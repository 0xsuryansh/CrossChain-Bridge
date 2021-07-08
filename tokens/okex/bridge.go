package okex

import (
	"math/big"
	"strings"
	"time"

	"github.com/anyswap/CrossChain-Bridge/log"
	"github.com/anyswap/CrossChain-Bridge/tokens"
	"github.com/anyswap/CrossChain-Bridge/tokens/eth"
	"github.com/anyswap/CrossChain-Bridge/types"
)

// Bridge okex bridge inherit from eth bridge
type Bridge struct {
	*eth.Bridge
}

// NewCrossChainBridge new okex bridge
func NewCrossChainBridge(isSrc bool) *Bridge {
	bridge := &Bridge{Bridge: eth.NewCrossChainBridge(isSrc)}
	bridge.Inherit = bridge
	return bridge
}

// SetChainAndGateway set token and gateway config
func (b *Bridge) SetChainAndGateway(chainCfg *tokens.ChainConfig, gatewayCfg *tokens.GatewayConfig) {
	b.CrossChainBridgeBase.SetChainAndGateway(chainCfg, gatewayCfg)
	b.VerifyChainID()
	b.Init()
}

// VerifyChainID verify chain id
func (b *Bridge) VerifyChainID() {
	networkID := strings.ToLower(b.ChainConfig.NetID)
	targetChainID := eth.GetChainIDOfNetwork(eth.OkexNetworkAndChainIDMap, networkID)
	isCustom := eth.IsCustomNetwork(networkID)
	if !isCustom && targetChainID == nil {
		log.Fatalf("unsupported okex network: %v", b.ChainConfig.NetID)
	}

	var (
		chainID *big.Int
		err     error
	)

	for {
		chainID, err = b.GetSignerChainID()
		if err == nil {
			break
		}
		log.Errorf("can not get gateway chainID. %v", err)
		log.Println("retry query gateway", b.GatewayConfig.APIAddress)
		time.Sleep(3 * time.Second)
	}

	if !isCustom && chainID.Cmp(targetChainID) != 0 {
		log.Fatalf("gateway chainID '%v' is not '%v'", chainID, b.ChainConfig.NetID)
	}

	b.SignerChainID = chainID
	b.Signer = types.MakeSigner("EIP155", chainID)

	log.Info("VerifyChainID succeed", "networkID", networkID, "chainID", chainID)
}
