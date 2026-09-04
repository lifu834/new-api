package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这些用例锁死同一条不变量: 任何"读整行 -> 改一个字段 -> 写回整行"的路径
// 都不得把读取瞬间的余额快照盖回数据库。并发扣费一旦被这样覆盖,用户只要
// 在计费过程中改个语言/取一次 Key 就能让额度回滚。

func seedUser(t *testing.T, username string, quota int) *User {
	t.Helper()
	user := &User{
		Username:    username,
		Password:    "hashed-placeholder",
		DisplayName: username,
		Quota:       quota,
		Group:       "default",
		AffCode:     username, // aff_code 是唯一索引, 每个用例给个不同值
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func quotaOf(t *testing.T, id int) int {
	t.Helper()
	var fresh User
	require.NoError(t, DB.First(&fresh, id).Error)
	return fresh.Quota
}

// 模拟并发扣费: 拿到快照之后、写回之前, 别的请求把额度扣掉了。
func spend(t *testing.T, id int, amount int) {
	t.Helper()
	require.NoError(t, DB.Model(&User{}).Where("id = ?", id).
		Update("quota", DB.Raw("quota - ?", amount)).Error)
}

func TestUpdateDoesNotRollbackQuota(t *testing.T) {
	user := seedUser(t, "writeback-quota", 1000)
	snapshot := *user // 请求开始时读到的旧快照

	spend(t, user.Id, 600)
	require.Equal(t, 400, quotaOf(t, user.Id))

	snapshot.DisplayName = "改个昵称"
	require.NoError(t, snapshot.Update(false))

	assert.Equal(t, 400, quotaOf(t, user.Id), "额度被旧快照写回 = 额度回滚")

	var fresh User
	require.NoError(t, DB.First(&fresh, user.Id).Error)
	assert.Equal(t, "改个昵称", fresh.DisplayName, "非余额字段仍应正常落库")
}

func TestUpdateDoesNotRollbackAffQuota(t *testing.T) {
	user := seedUser(t, "writeback-aff", 1000)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"aff_count": 1, "aff_quota": 500, "aff_history": 500,
	}).Error)

	var snapshot User
	require.NoError(t, DB.First(&snapshot, user.Id).Error)

	// 快照之后把返佣额度转走
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).
		Update("aff_quota", 0).Error)

	snapshot.DisplayName = "转走之后再写回"
	require.NoError(t, snapshot.Update(false))

	var fresh User
	require.NoError(t, DB.First(&fresh, user.Id).Error)
	assert.Equal(t, 0, fresh.AffQuota, "返佣余额被旧快照写回 = 可反复刷返佣")
}

func TestUpdateUserSettingKeepsQuota(t *testing.T) {
	user := seedUser(t, "writeback-setting", 1000)
	snapshot := *user

	spend(t, user.Id, 900)

	setting := snapshot.GetSetting()
	setting.Language = "ja"
	require.NoError(t, UpdateUserSetting(snapshot.Id, setting))

	assert.Equal(t, 100, quotaOf(t, user.Id), "改语言不得影响额度")

	var fresh User
	require.NoError(t, DB.First(&fresh, user.Id).Error)
	assert.Equal(t, "ja", fresh.GetSetting().Language)
}

func TestUpdateUserAccessTokenKeepsQuota(t *testing.T) {
	user := seedUser(t, "writeback-token", 1000)

	spend(t, user.Id, 250)
	require.NoError(t, UpdateUserAccessToken(user.Id, "brand-new-token"))

	assert.Equal(t, 750, quotaOf(t, user.Id), "取 Key 不得影响额度")

	var fresh User
	require.NoError(t, DB.First(&fresh, user.Id).Error)
	require.NotNil(t, fresh.AccessToken)
	assert.Equal(t, "brand-new-token", *fresh.AccessToken)
}

func TestUpdateUserSettingRejectsMissingUser(t *testing.T) {
	assert.Error(t, UpdateUserSetting(0, dto.UserSetting{}))
	assert.Error(t, UpdateUserSetting(9999999, dto.UserSetting{}))
	assert.Error(t, UpdateUserAccessToken(0, "x"))
	assert.Error(t, UpdateUserAccessToken(9999999, "x"))
}

func TestInviteUserIncrementsAtomically(t *testing.T) {
	original := common.QuotaForInviter
	common.QuotaForInviter = 300
	defer func() { common.QuotaForInviter = original }()

	inviter := seedUser(t, "writeback-inviter", 1000)
	spend(t, inviter.Id, 700)

	require.NoError(t, inviteUser(inviter.Id))

	var fresh User
	require.NoError(t, DB.First(&fresh, inviter.Id).Error)
	assert.Equal(t, 300, fresh.Quota, "返佣入账不得改动主额度")
	assert.Equal(t, 1, fresh.AffCount)
	assert.Equal(t, 300, fresh.AffQuota)
	assert.Equal(t, 300, fresh.AffHistoryQuota)

	require.NoError(t, inviteUser(inviter.Id))
	require.NoError(t, DB.First(&fresh, inviter.Id).Error)
	assert.Equal(t, 2, fresh.AffCount, "第二次邀请应继续累加")
	assert.Equal(t, 600, fresh.AffQuota)
}

// 并发邀请不得丢更新。读整行->改->写回整行的写法在并发下会互相覆盖,
// 只有 SQL 侧原子自增才能保证每一次邀请都算数。
func TestInviteUserConcurrentNoLostUpdate(t *testing.T) {
	original := common.QuotaForInviter
	common.QuotaForInviter = 10
	defer func() { common.QuotaForInviter = original }()

	inviter := seedUser(t, "writeback-inviter-concurrent", 1000)

	const invites = 30
	var wg sync.WaitGroup
	errs := make(chan error, invites)
	for i := 0; i < invites; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := inviteUser(inviter.Id); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var fresh User
	require.NoError(t, DB.First(&fresh, inviter.Id).Error)
	assert.Equal(t, invites, fresh.AffCount, "并发邀请丢了更新")
	assert.Equal(t, invites*10, fresh.AffQuota)
	assert.Equal(t, invites*10, fresh.AffHistoryQuota)
	assert.Equal(t, 1000, fresh.Quota, "邀请返佣不得改动主额度")
}
