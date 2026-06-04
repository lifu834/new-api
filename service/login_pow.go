package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// 登录自适应 PoW（反撞库）。
// 协议（前端 utils/pow.ts 必须一致）：
//   challenge = { id, seed(hex), difficulty(前导零 bit 数), expires_at }
//   求解：找 nonce 使 SHA256(seed + ":" + nonce) 的二进制有 difficulty 个前导零 bit
//   校验：服务端重算、单次有效、2 分钟过期
//
// 存储为进程内内存（单节点正确；challenge 与 login 在同一 new-api 进程内完成）。
// 仅当 common.LoginPowEnabled 时，Login 才会调用本模块；默认关闭=对正常用户零影响。

const powTTL = 2 * time.Minute
const loginFailTTL = 15 * time.Minute

type powEntry struct {
	seed      string
	expiresAt time.Time
}

type failEntry struct {
	count     int
	expiresAt time.Time
}

var (
	powMu      sync.Mutex
	powPending = map[string]*powEntry{}

	failMu    sync.Mutex
	failCount = map[string]*failEntry{}

	powCleanupOnce sync.Once
)

func startPowCleanup() {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			powMu.Lock()
			for id, e := range powPending {
				if now.After(e.expiresAt) {
					delete(powPending, id)
				}
			}
			powMu.Unlock()
			failMu.Lock()
			for k, e := range failCount {
				if now.After(e.expiresAt) {
					delete(failCount, k)
				}
			}
			failMu.Unlock()
		}
	}()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// IssueLoginPowChallenge 签发一个 PoW 挑战。
func IssueLoginPowChallenge() (id, seed string, difficulty int, expiresAt int64) {
	powCleanupOnce.Do(startPowCleanup)
	id = randHex(16)
	seed = randHex(32)
	exp := time.Now().Add(powTTL)
	powMu.Lock()
	powPending[id] = &powEntry{seed: seed, expiresAt: exp}
	powMu.Unlock()
	return id, seed, common.LoginPowDifficulty, exp.Unix()
}

// VerifyLoginPowSolution 校验 PoW（单次有效）。
func VerifyLoginPowSolution(challengeID, nonce string) bool {
	if challengeID == "" || nonce == "" {
		return false
	}
	powMu.Lock()
	e, ok := powPending[challengeID]
	if ok {
		delete(powPending, challengeID) // 单次使用
	}
	powMu.Unlock()
	if !ok || time.Now().After(e.expiresAt) {
		return false
	}
	h := sha256.Sum256([]byte(e.seed + ":" + nonce))
	return hasLeadingZeroBits(h[:], common.LoginPowDifficulty)
}

func hasLeadingZeroBits(hash []byte, bits int) bool {
	full := bits / 8
	rem := bits % 8
	for i := 0; i < full; i++ {
		if i >= len(hash) || hash[i] != 0 {
			return false
		}
	}
	if rem > 0 && full < len(hash) {
		if hash[full]&(byte(0xFF)<<(8-rem)) != 0 {
			return false
		}
	}
	return true
}

// LoginFailCount 返回某用户名当前失败计数（过期自动归零）。
func LoginFailCount(username string) int {
	failMu.Lock()
	defer failMu.Unlock()
	e, ok := failCount[username]
	if !ok || time.Now().After(e.expiresAt) {
		return 0
	}
	return e.count
}

// IncrLoginFail 登录失败 +1。
func IncrLoginFail(username string) {
	failMu.Lock()
	defer failMu.Unlock()
	e, ok := failCount[username]
	if !ok || time.Now().After(e.expiresAt) {
		failCount[username] = &failEntry{count: 1, expiresAt: time.Now().Add(loginFailTTL)}
		return
	}
	e.count++
	e.expiresAt = time.Now().Add(loginFailTTL)
}

// ResetLoginFail 登录成功后清零。
func ResetLoginFail(username string) {
	failMu.Lock()
	delete(failCount, username)
	failMu.Unlock()
}
