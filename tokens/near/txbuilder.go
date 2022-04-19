package near

import (
	"encoding/base64"
	"fmt"

	"github.com/gogo/protobuf/proto"

	"github.com/cosmos/cosmos-sdk/client"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"

	"github.com/anyswap/CrossChain-Bridge/common"
)

// TxBuilder is a wrapper around the tx.Tx proto.Message which retain the raw
// body and auth_info bytes.
type TxBuilder struct {
	tx *tx.Tx

	// bodyBz represents the protobuf encoding of TxBody. This should be encoding
	// from the client using TxRaw if the tx was decoded from the wire
	bodyBz []byte

	// authInfoBz represents the protobuf encoding of TxBody. This should be encoding
	// from the client using TxRaw if the tx was decoded from the wire
	authInfoBz []byte

	// signerData is the specific information needed to sign a transaction that generally
	// isn't included in the transaction body itself
	signerData *authsigning.SignerData
}

var (
	_ authsigning.Tx             = &TxBuilder{}
	_ client.TxBuilder           = &TxBuilder{}
	_ ante.HasExtensionOptionsTx = &TxBuilder{}
	_ ExtensionOptionsTxBuilder  = &TxBuilder{}
)

// ExtensionOptionsTxBuilder defines a TxBuilder that can also set extensions.
type ExtensionOptionsTxBuilder interface {
	client.TxBuilder

	SetExtensionOptions(...*codectypes.Any)
	SetNonCriticalExtensionOptions(...*codectypes.Any)
}

func newBuilder() *TxBuilder {
	return &TxBuilder{
		tx: &tx.Tx{
			Body: &tx.TxBody{},
			AuthInfo: &tx.AuthInfo{
				Fee: &tx.Fee{},
			},
		},
	}
}

// GetMsgs impl
func (w *TxBuilder) GetMsgs() []sdk.Msg {
	return w.tx.GetMsgs()
}

// ValidateBasic impl
func (w *TxBuilder) ValidateBasic() error {
	return w.tx.ValidateBasic()
}

func (w *TxBuilder) getBodyBytes() []byte {
	if len(w.bodyBz) == 0 {
		// if bodyBz is empty, then marshal the body. bodyBz will generally
		// be set to nil whenever SetBody is called so the result of calling
		// this method should always return the correct bytes. Note that after
		// decoding bodyBz is derived from TxRaw so that it matches what was
		// transmitted over the wire
		var err error
		w.bodyBz, err = proto.Marshal(w.tx.Body)
		if err != nil {
			panic(err)
		}
	}
	return w.bodyBz
}

func (w *TxBuilder) getAuthInfoBytes() []byte {
	if len(w.authInfoBz) == 0 {
		// if authInfoBz is empty, then marshal the body. authInfoBz will generally
		// be set to nil whenever SetAuthInfo is called so the result of calling
		// this method should always return the correct bytes. Note that after
		// decoding authInfoBz is derived from TxRaw so that it matches what was
		// transmitted over the wire
		var err error
		w.authInfoBz, err = proto.Marshal(w.tx.AuthInfo)
		if err != nil {
			panic(err)
		}
	}
	return w.authInfoBz
}

// GetSigners impl
func (w *TxBuilder) GetSigners() []sdk.AccAddress {
	return w.tx.GetSigners()
}

// GetPubkeys returns the pubkeys of signers if the pubkey is included in the signature
// If pubkey is not included in the signature, then nil is in the slice instead
func (w *TxBuilder) GetPubKeys() ([]cryptotypes.PubKey, error) {
	signerInfos := w.tx.AuthInfo.SignerInfos
	pks := make([]cryptotypes.PubKey, len(signerInfos))

	for i, si := range signerInfos {
		// NOTE: it is okay to leave this nil if there is no PubKey in the SignerInfo.
		// PubKey's can be left unset in SignerInfo.
		if si.PublicKey == nil {
			continue
		}

		pkAny := si.PublicKey.GetCachedValue()
		pk, ok := pkAny.(cryptotypes.PubKey)
		if ok {
			pks[i] = pk
		} else {
			return nil, sdkerrors.Wrapf(sdkerrors.ErrLogic, "Expecting PubKey, got: %T", pkAny)
		}
	}

	return pks, nil
}

// GetGas get gas
func (w *TxBuilder) GetGas() uint64 {
	return w.tx.AuthInfo.Fee.GasLimit
}

// GetFee get fee
func (w *TxBuilder) GetFee() sdk.Coins {
	return w.tx.AuthInfo.Fee.Amount
}

// FeePayer get fee payer
func (w *TxBuilder) FeePayer() sdk.AccAddress {
	feePayer := w.tx.AuthInfo.Fee.Payer
	if feePayer != "" {
		payerAddr, err := sdk.AccAddressFromBech32(feePayer)
		if err != nil {
			panic(err)
		}
		return payerAddr
	}
	// use first signer as default if no payer specified
	return w.GetSigners()[0]
}

// FeeGranter get fee granter
func (w *TxBuilder) FeeGranter() sdk.AccAddress {
	feePayer := w.tx.AuthInfo.Fee.Granter
	if feePayer != "" {
		granterAddr, err := sdk.AccAddressFromBech32(feePayer)
		if err != nil {
			panic(err)
		}
		return granterAddr
	}
	return nil
}

// GetMemo get memo
func (w *TxBuilder) GetMemo() string {
	return w.tx.Body.Memo
}

// GetTimeoutHeight returns the transaction's timeout height (if set).
func (w *TxBuilder) GetTimeoutHeight() uint64 {
	return w.tx.Body.TimeoutHeight
}

// GetSignaturesV2 get signature v2
func (w *TxBuilder) GetSignaturesV2() ([]signing.SignatureV2, error) {
	signerInfos := w.tx.AuthInfo.SignerInfos
	sigs := w.tx.Signatures
	pubKeys, err := w.GetPubKeys()
	if err != nil {
		return nil, err
	}
	n := len(signerInfos)
	res := make([]signing.SignatureV2, n)

	for i, si := range signerInfos {
		// handle nil signatures (in case of simulation)
		if si.ModeInfo == nil {
			res[i] = signing.SignatureV2{
				PubKey: pubKeys[i],
			}
		} else {
			var err error
			sigData, err := authtx.ModeInfoAndSigToSignatureData(si.ModeInfo, sigs[i])
			if err != nil {
				return nil, err
			}
			res[i] = signing.SignatureV2{
				PubKey:   pubKeys[i],
				Data:     sigData,
				Sequence: si.GetSequence(),
			}
		}
	}

	return res, nil
}

// SetMsgs set msgs
func (w *TxBuilder) SetMsgs(msgs ...sdk.Msg) error {
	return nil
}

// SetTimeoutHeight sets the transaction's height timeout.
func (w *TxBuilder) SetTimeoutHeight(height uint64) {
}

// SetMemo set memo
func (w *TxBuilder) SetMemo(memo string) {
}

// SetGasLimit set gas limit
func (w *TxBuilder) SetGasLimit(limit uint64) {

}

// SetFeeAmount set fee amount
func (w *TxBuilder) SetFeeAmount(coins sdk.Coins) {

}

// SetFeePayer set fee payer
func (w *TxBuilder) SetFeePayer(feePayer sdk.AccAddress) {

}

// SetFeeGranter set fee granter
func (w *TxBuilder) SetFeeGranter(feeGranter sdk.AccAddress) {

}

// SetSignatures set signatures
func (w *TxBuilder) SetSignatures(signatures ...signing.SignatureV2) error {

	return nil
}

func (w *TxBuilder) setSignerInfos(infos []*tx.SignerInfo) {

}

func (w *TxBuilder) setSignatures(sigs [][]byte) {
}

// GetTx get tx
func (w *TxBuilder) GetTx() authsigning.Tx {
	return w
}

// GetProtoTx get proto tx
func (w *TxBuilder) GetProtoTx() *tx.Tx {
	return w.tx
}

// WrapTx creates a TxBuilder TxBuilder around a tx.Tx proto message.
func WrapTx(protoTx *tx.Tx) client.TxBuilder {
	return &TxBuilder{
		tx: protoTx,
	}
}

// GetExtensionOptions get extension options
func (w *TxBuilder) GetExtensionOptions() []*codectypes.Any {
	return w.tx.Body.ExtensionOptions
}

// GetNonCriticalExtensionOptions get non critical extension options
func (w *TxBuilder) GetNonCriticalExtensionOptions() []*codectypes.Any {
	return w.tx.Body.NonCriticalExtensionOptions
}

// SetExtensionOptions set extension options
func (w *TxBuilder) SetExtensionOptions(extOpts ...*codectypes.Any) {
	w.tx.Body.ExtensionOptions = extOpts
	w.bodyBz = nil
}

// SetNonCriticalExtensionOptions set non critical extension options
func (w *TxBuilder) SetNonCriticalExtensionOptions(extOpts ...*codectypes.Any) {
	w.tx.Body.NonCriticalExtensionOptions = extOpts
	w.bodyBz = nil
}

// GetSignerData get signer data
func (w *TxBuilder) GetSignerData() *authsigning.SignerData {
	return w.signerData
}

// SetSignerData set signer data
func (w *TxBuilder) SetSignerData(chainID string, accountNumber, sequence uint64) {
	w.signerData = &authsigning.SignerData{
		ChainID:       chainID,
		AccountNumber: accountNumber,
		Sequence:      sequence,
	}
}

// GetSignBytes get sign bytes
func (w *TxBuilder) GetSignBytes() ([]byte, error) {
	return authtx.DirectSignBytes(
		w.getBodyBytes(),
		w.getAuthInfoBytes(),
		w.signerData.ChainID,
		w.signerData.AccountNumber)
}

// GetSignedTx get signed raw tx to broadcast
func (w *TxBuilder) GetSignedTx() (signedTx []byte, txHash string, err error) {
	txBytes, err := w.GetProtoTx().Marshal()
	if err != nil {
		return nil, "", err
	}
	signedTx = []byte(base64.StdEncoding.EncodeToString(txBytes))
	txHash = fmt.Sprintf("%X", common.Sha256Sum(txBytes))
	return signedTx, txHash, nil
}

// GetTxBytes get tx marshal bytes
func (w *TxBuilder) GetTxBytes() ([]byte, error) {
	return w.GetProtoTx().Marshal()
}
