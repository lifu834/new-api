package setting

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var ModelRequestRateLimitEnabled = false
var ModelRequestRateLimitDurationMinutes = 1
var ModelRequestRateLimitCount = 0
var ModelRequestRateLimitSuccessCount = 1000
var ModelRequestRateLimitGroup = map[string][2]int{}
var ModelRequestRateLimitMutex sync.RWMutex

func ModelRequestRateLimitGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := json.Marshal(ModelRequestRateLimitGroup)
	if err != nil {
		common.SysLog("error marshalling model ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRequestRateLimitGroupByJSONString(jsonStr string) error {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	ModelRequestRateLimitGroup = make(map[string][2]int)
	return json.Unmarshal([]byte(jsonStr), &ModelRequestRateLimitGroup)
}

func GetGroupRateLimit(group string) (totalCount, successCount int, found bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestRateLimitGroup == nil {
		return 0, 0, false
	}

	limits, found := ModelRequestRateLimitGroup[group]
	if !found {
		return 0, 0, false
	}
	return limits[0], limits[1], true
}

func CheckModelRequestRateLimitGroup(jsonStr string) error {
	checkModelRequestRateLimitGroup := make(map[string][2]int)
	err := json.Unmarshal([]byte(jsonStr), &checkModelRequestRateLimitGroup)
	if err != nil {
		return err
	}
	for group, limits := range checkModelRequestRateLimitGroup {
		if limits[0] < 0 || limits[1] < 1 {
			return fmt.Errorf("group %s has negative rate limit values: [%d, %d]", group, limits[0], limits[1])
		}
		if limits[0] > math.MaxInt32 || limits[1] > math.MaxInt32 {
			return fmt.Errorf("group %s [%d, %d] has max rate limits value 2147483647", group, limits[0], limits[1])
		}
	}

	return nil
}

// ===== nycatai: 模型并发限制（按用户身份档位，default/enterprise）=====

var ModelConcurrencyLimitEnabled = false
var ModelConcurrencyLimitCount = 0                // 全局默认最大并发，0 = 不限制
var ModelConcurrencyLimitGroup = map[string]int{} // 身份分组 -> 最大并发
var ModelConcurrencyLimitMutex sync.RWMutex

func ModelConcurrencyLimitGroup2JSONString() string {
	ModelConcurrencyLimitMutex.RLock()
	defer ModelConcurrencyLimitMutex.RUnlock()

	jsonBytes, err := json.Marshal(ModelConcurrencyLimitGroup)
	if err != nil {
		common.SysLog("error marshalling model concurrency limit: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelConcurrencyLimitGroupByJSONString(jsonStr string) error {
	ModelConcurrencyLimitMutex.Lock()
	defer ModelConcurrencyLimitMutex.Unlock()

	ModelConcurrencyLimitGroup = make(map[string]int)
	return json.Unmarshal([]byte(jsonStr), &ModelConcurrencyLimitGroup)
}

func GetGroupConcurrencyLimit(group string) (int, bool) {
	ModelConcurrencyLimitMutex.RLock()
	defer ModelConcurrencyLimitMutex.RUnlock()

	if ModelConcurrencyLimitGroup == nil {
		return 0, false
	}
	v, ok := ModelConcurrencyLimitGroup[group]
	return v, ok
}

// ===== nycatai: 限流豁免分组白名单（按用户身份档位，如 vvip；RPM + 并发全跳过）=====

var ModelRateLimitExemptGroups = map[string]bool{}
var ModelRateLimitExemptMutex sync.RWMutex

// ModelRateLimitExemptGroups2JSONString 序列化为排序后的 JSON 字符串数组，如 ["vvip"]
func ModelRateLimitExemptGroups2JSONString() string {
	ModelRateLimitExemptMutex.RLock()
	defer ModelRateLimitExemptMutex.RUnlock()

	groups := make([]string, 0, len(ModelRateLimitExemptGroups))
	for g := range ModelRateLimitExemptGroups {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	jsonBytes, err := common.Marshal(groups)
	if err != nil {
		common.SysLog("error marshalling rate limit exempt groups: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRateLimitExemptGroupsByJSONString(jsonStr string) error {
	var groups []string
	if err := common.Unmarshal([]byte(jsonStr), &groups); err != nil {
		return err
	}
	m := make(map[string]bool, len(groups))
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g != "" {
			m[g] = true
		}
	}
	ModelRateLimitExemptMutex.Lock()
	defer ModelRateLimitExemptMutex.Unlock()
	ModelRateLimitExemptGroups = m
	return nil
}

func IsModelRateLimitExemptGroup(group string) bool {
	if group == "" {
		return false
	}
	ModelRateLimitExemptMutex.RLock()
	defer ModelRateLimitExemptMutex.RUnlock()
	return ModelRateLimitExemptGroups[group]
}

func CheckModelRateLimitExemptGroups(jsonStr string) error {
	var groups []string
	if err := common.Unmarshal([]byte(jsonStr), &groups); err != nil {
		return fmt.Errorf(`豁免分组须为 JSON 字符串数组，如 ["vvip"]: %s`, err.Error())
	}
	for _, g := range groups {
		if strings.TrimSpace(g) == "" {
			return fmt.Errorf("豁免分组名不能为空")
		}
	}
	return nil
}

func CheckModelConcurrencyLimitGroup(jsonStr string) error {
	m := make(map[string]int)
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return err
	}
	for group, v := range m {
		if v < 0 {
			return fmt.Errorf("group %s has negative concurrency value: %d", group, v)
		}
		if v > math.MaxInt32 {
			return fmt.Errorf("group %s concurrency %d exceeds max 2147483647", group, v)
		}
	}
	return nil
}
