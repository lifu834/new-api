package model

import (
	"github.com/QuantumNous/new-api/common"
)

// GetUserRechargeQuota 返回用户的累计充值额度（quota 单位，非人民币）。
//
// 会员档位按累计充值核定（260908 拍板，此前用累计消费 users.used_quota）。
// 两条资金入口都算：
//   - redemptions：兑换码核销。**核销人在 used_user_id，不是 user_id**——后者是建码的
//     管理员，用错会把全站充值都算到 admin 头上。
//   - top_ups：在线支付。当前线上该表为空（资金主通道是兑换码），但接了支付就会有，
//     所以一并计入，免得日后漏算。
//
// 不计入的：邀请返佣（aff_quota / aff_history_quota，那是返利不是充值）、
// 管理员手工调额（无独立流水表，无从区分补偿与充值）、新用户注册赠额。
//
// 口径与 nycatai-ops/scripts/tier-scan.py 必须一致：那边是发档的权威，这里只是给
// 用户看进度。两处都改才算改完。
func GetUserRechargeQuota(userId int) (int64, error) {
	var redeemed int64
	err := DB.Table("redemptions").
		Where("used_user_id = ? AND status = ?", userId, common.RedemptionCodeStatusUsed).
		Select("COALESCE(SUM(quota), 0)").
		Scan(&redeemed).Error
	if err != nil {
		return 0, err
	}

	var toppedUp int64
	err = DB.Table("top_ups").
		Where("user_id = ? AND status = ?", userId, common.TopUpStatusSuccess).
		Select("COALESCE(SUM(amount), 0)").
		Scan(&toppedUp).Error
	if err != nil {
		return 0, err
	}

	return redeemed + toppedUp, nil
}
