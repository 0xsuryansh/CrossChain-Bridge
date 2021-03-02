package cosmos

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/tendermint/tendermint/crypto/tmhash"
)

// StdSignContent saves all tx components required to build SignBytes
type StdSignContent struct {
	AccountNumber uint64
	ChainID       string
	Fee           authtypes.StdFee
	Memo          string
	Msgs          []sdk.Msg
	Sequence      uint64
}

// HashableStdTx saves all data of a signed tx
type HashableStdTx struct {
	StdSignContent
	Signatures []authtypes.StdSignature
}

// SignBytes returns sign bytes
func (tx StdSignContent) SignBytes() []byte {
	return authtypes.StdSignBytes(tx.ChainID, tx.AccountNumber, tx.Sequence, tx.Fee, tx.Msgs, tx.Memo)
}

// Hash returns tx hash string
func (tx StdSignContent) Hash() string {
	signBytes := authtypes.StdSignBytes(tx.ChainID, tx.AccountNumber, tx.Sequence, tx.Fee, tx.Msgs, tx.Memo)
	txHash := fmt.Sprintf("%X", tmhash.Sum(signBytes))
	return txHash
}

// ToStdTx converts HashableStdTx to authtypes.StdTx
func (tx HashableStdTx) ToStdTx() authtypes.StdTx {
	return authtypes.StdTx{
		Msgs:       tx.Msgs,
		Fee:        tx.Fee,
		Signatures: tx.Signatures,
		Memo:       tx.Memo,
	}
}
