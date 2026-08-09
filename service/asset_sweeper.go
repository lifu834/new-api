package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

// assetSweepInterval 是清理周期。素材保留 7 天，一小时扫一次足够，
// 也不会因为扫描太频繁给数据库添负担。
const assetSweepInterval = time.Hour

// StartAssetSweeper 周期性回收过期/已删素材的磁盘文件与数据库记录。
//
// 顺序很重要：**先删磁盘、再删记录**。反过来一旦中途失败，磁盘文件就变成
// 无主垃圾——没有任何记录指向它，只能靠人工翻目录才能发现。
func StartAssetSweeper() {
	go func() {
		// 启动后稍等，避开迁移与初始化
		time.Sleep(time.Minute)
		for {
			sweepAssetsOnce()
			time.Sleep(assetSweepInterval)
		}
	}()
}

func sweepAssetsOnce() {
	defer func() {
		if r := recover(); r != nil {
			logger.LogError(context.Background(),
				common.Interface2String(r))
		}
	}()

	items, err := model.SweepableAssets(500)
	if err != nil {
		logger.LogError(context.Background(), "asset sweeper query failed: "+err.Error())
		return
	}
	if len(items) == 0 {
		return
	}

	purgeable := make([]int, 0, len(items))
	for _, a := range items {
		if err := RemoveAsset(a.AssetKey, a.CreatedAt); err != nil {
			// 磁盘没删掉就**不要**删记录，否则文件会变成无主垃圾
			logger.LogError(context.Background(),
				"asset sweeper remove file failed: "+a.AssetKey+": "+err.Error())
			continue
		}
		purgeable = append(purgeable, a.Id)
	}
	if err := model.PurgeAssetRows(purgeable); err != nil {
		logger.LogError(context.Background(), "asset sweeper purge rows failed: "+err.Error())
		return
	}
	logger.LogInfo(context.Background(),
		"asset sweeper removed "+common.Interface2String(len(purgeable))+" expired assets")
}
