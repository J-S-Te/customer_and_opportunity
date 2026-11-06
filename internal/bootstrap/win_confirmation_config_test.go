package bootstrap

import "testing"

// TestWinConfirmationRejectsUnknownMode 验证赢单模式必须显式受支持。
// 参数：t 为当前测试上下文。
// 返回值：无；未知模式被忽略时测试失败。
func TestWinConfirmationRejectsUnknownMode(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("CRM_WIN_CONFIRMATION_MODE", "skip_contract")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("未知赢单模式未被拒绝")
	}
}

// TestWinConfirmationCapabilitiesSeparateModeAndSubscription 验证本地证据与转单订阅独立。
// 参数：t 为当前测试上下文。
// 返回值：无；模式缺失或假开放转单时测试失败。
func TestWinConfirmationCapabilitiesSeparateModeAndSubscription(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("CRM_WIN_CONFIRMATION_MODE", "local_evidence")
	t.Setenv("CRM_CONTRACT_TRANSFER_SUBSCRIBED", "false")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	capabilities := runtimeCapabilities(config)
	if !capabilities["win_confirmation_local_evidence"].Available {
		t.Fatal("显式本地赢单模式没有可用能力")
	}
	if capabilities["contract_transfer"].Available {
		t.Fatal("未订阅合同转单仍被开放")
	}
}

// TestWinConfirmationDefaultsRemainCompatible 验证未配置时保留合同模式和原订阅行为。
// 参数：t 为当前测试上下文。
// 返回值：无；默认行为发生漂移时失败。
func TestWinConfirmationDefaultsRemainCompatible(t *testing.T) {
	setBaseConfig(t)
	t.Setenv("CRM_WIN_CONFIRMATION_MODE", "")
	t.Setenv("CRM_CONTRACT_TRANSFER_SUBSCRIBED", "")
	config, err := LoadConfig()
	if err != nil || config.WinConfirmationMode != "contract" || !config.ContractTransferSubscribed {
		t.Fatalf("默认赢单模式或订阅变化: mode=%s subscribed=%t err=%v", config.WinConfirmationMode, config.ContractTransferSubscribed, err)
	}
}
