package view

import (
	"testing"
	"time"
)

// TestSameBatchRequest 连点去抖的核心规则：
// 只有「同签名 + 窗口内」才算重复；换动作、换仓库、超窗口都要放行。
func TestSameBatchRequest(t *testing.T) {
	now := time.Now()

	if sameBatchRequest("", time.Time{}, "刷新状态|1,2", now) {
		t.Fatal("第一次点击不应被判定为重复")
	}
	if !sameBatchRequest("刷新状态|1,2", now, "刷新状态|1,2", now.Add(100*time.Millisecond)) {
		t.Fatal("100ms 内的同参数点击应被拦下")
	}
	if sameBatchRequest("刷新状态|1,2", now, "刷新状态|1,2", now.Add(batchClickDebounce)) {
		t.Fatal("超过去抖窗口后应允许再次执行")
	}
	if sameBatchRequest("刷新状态|1,2", now, "刷新远端信息|1,2", now.Add(50*time.Millisecond)) {
		t.Fatal("换了动作不应被当成重复点击")
	}
	if sameBatchRequest("刷新状态|1,2", now, "刷新状态|3", now.Add(50*time.Millisecond)) {
		t.Fatal("换了仓库集合不应被当成重复点击")
	}
}

// TestBatchSignatureIgnoresOrder 仓库顺序不同但集合相同，签名必须一致，
// 否则「全选」与「逐个选」会被当成两次不同的请求。
func TestBatchSignatureIgnoresOrder(t *testing.T) {
	a := batchSignature(batchInspect, []int64{3, 1, 2})
	b := batchSignature(batchInspect, []int64{1, 2, 3})
	if a != b {
		t.Fatalf("签名应与顺序无关：%q vs %q", a, b)
	}
	if batchSignature(batchPull, []int64{1, 2, 3}) == a {
		t.Fatal("不同动作的签名不应相同")
	}
}
