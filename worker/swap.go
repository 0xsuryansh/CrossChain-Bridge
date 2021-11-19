package worker

import (
	"container/ring"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/anyswap/CrossChain-Bridge/mongodb"
	"github.com/anyswap/CrossChain-Bridge/params"
	"github.com/anyswap/CrossChain-Bridge/tokens"
	mapset "github.com/deckarep/golang-set"
)

var (
	swapRing        *ring.Ring
	swapRingLock    sync.RWMutex
	swapRingMaxSize = 1000

	cachedSwapTasks    = mapset.NewSet()
	maxCachedSwapTasks = 1000

	swapChanSize       = 10
	swapinTaskChanMap  = make(map[string]chan *tokens.BuildTxArgs)
	swapoutTaskChanMap = make(map[string]chan *tokens.BuildTxArgs)

	errAlreadySwapped     = errors.New("already swapped")
	errSendTxWithDiffHash = errors.New("send tx with different hash")
)

// StartSwapJob swap job
func StartSwapJob() {
	swapinNonces, swapoutNonces := mongodb.LoadAllSwapNonces()
	if nonceSetter, ok := tokens.DstBridge.(tokens.NonceSetter); ok {
		nonceSetter.InitNonces(swapinNonces)
	}
	if nonceSetter, ok := tokens.SrcBridge.(tokens.NonceSetter); ok {
		nonceSetter.InitNonces(swapoutNonces)
	}
	for _, pairCfg := range tokens.GetTokenPairsConfig() {
		AddSwapJob(pairCfg)
	}

	go startSwapinSwapJob()
	go startSwapoutSwapJob()
}

// AddSwapJob add swap job
func AddSwapJob(pairCfg *tokens.TokenPairConfig) {
	swapinDcrmAddr := strings.ToLower(pairCfg.DestToken.DcrmAddress)
	if _, exist := swapinTaskChanMap[swapinDcrmAddr]; !exist {
		swapinTaskChanMap[swapinDcrmAddr] = make(chan *tokens.BuildTxArgs, swapChanSize)
		go processSwapTask(swapinTaskChanMap[swapinDcrmAddr])
	}
	swapoutDcrmAddr := strings.ToLower(pairCfg.SrcToken.DcrmAddress)
	if _, exist := swapoutTaskChanMap[swapoutDcrmAddr]; !exist {
		swapoutTaskChanMap[swapoutDcrmAddr] = make(chan *tokens.BuildTxArgs, swapChanSize)
		go processSwapTask(swapoutTaskChanMap[swapoutDcrmAddr])
	}
}

func startSwapinSwapJob() {
	logWorker("swap", "start swapin swap job")
	for {
		res, err := findSwapinsToSwap()
		if err != nil {
			logWorkerError("swapin", "find swapins error", err)
		}
		if len(res) > 0 {
			logWorker("swapin", "find swapins to swap", "count", len(res))
		}
		for _, swap := range res {
			err = processSwapinSwap(swap)
			switch err {
			case nil, errAlreadySwapped:
			default:
				logWorkerError("swapin", "process swapin swap error", err, "pairID", swap.PairID, "txid", swap.TxID, "bind", swap.Bind)
			}
		}
		restInJob(restIntervalInDoSwapJob)
	}
}

func startSwapoutSwapJob() {
	logWorker("swapout", "start swapout swap job")
	for {
		res, err := findSwapoutsToSwap()
		if err != nil {
			logWorkerError("swapout", "find swapouts error", err)
		}
		if len(res) > 0 {
			logWorker("swapout", "find swapouts to swap", "count", len(res))
		}
		for _, swap := range res {
			err = processSwapoutSwap(swap)
			switch err {
			case nil, errAlreadySwapped:
			default:
				logWorkerError("swapout", "process swapout swap error", err, "pairID", swap.PairID, "txid", swap.TxID, "bind", swap.Bind)
			}
		}
		restInJob(restIntervalInDoSwapJob)
	}
}

func findSwapinsToSwap() ([]*mongodb.MgoSwap, error) {
	status := mongodb.TxNotSwapped
	septime := getSepTimeInFind(maxDoSwapLifetime)
	return mongodb.FindSwapinsWithStatus(status, septime)
}

func findSwapoutsToSwap() ([]*mongodb.MgoSwap, error) {
	status := mongodb.TxNotSwapped
	septime := getSepTimeInFind(maxDoSwapLifetime)
	return mongodb.FindSwapoutsWithStatus(status, septime)
}

func isSwapInBlacklist(swap *mongodb.MgoSwapResult) (isBlacked bool, err error) {
	isBlacked, err = mongodb.QueryBlacklist(swap.From, swap.PairID)
	if err != nil {
		return isBlacked, err
	}
	if !isBlacked && swap.Bind != swap.From {
		isBlacked, err = mongodb.QueryBlacklist(swap.Bind, swap.PairID)
		if err != nil {
			return isBlacked, err
		}
	}
	return isBlacked, nil
}

func processSwapinSwap(swap *mongodb.MgoSwap) (err error) {
	return processSwap(swap, true)
}

func processSwapoutSwap(swap *mongodb.MgoSwap) (err error) {
	return processSwap(swap, false)
}

func processSwap(swap *mongodb.MgoSwap, isSwapin bool) (err error) {
	pairID := swap.PairID
	txid := swap.TxID
	bind := swap.Bind

	cacheKey := getSwapCacheKey(isSwapin, txid, bind)
	if cachedSwapTasks.Contains(cacheKey) {
		return errAlreadySwapped
	}

	res, err := mongodb.FindSwapResult(isSwapin, txid, pairID, bind)
	if err != nil {
		return err
	}

	fromTokenCfg, toTokenCfg := tokens.GetTokenConfigsByDirection(pairID, isSwapin)
	if fromTokenCfg == nil || toTokenCfg == nil {
		logWorkerTrace("swap", "swap is not configed", "pairID", pairID, "isSwapin", isSwapin)
		return nil
	}
	if fromTokenCfg.DisableSwap {
		logWorkerTrace("swap", "swap is disabled", "pairID", pairID, "isSwapin", isSwapin)
		return nil
	}
	isBlacked, err := isSwapInBlacklist(res)
	if err != nil {
		return err
	}
	if isBlacked {
		logWorkerTrace("swap", "address is in blacklist", "txid", txid, "bind", bind, "isSwapin", isSwapin)
		err = tokens.ErrAddressIsInBlacklist
		_ = mongodb.UpdateSwapStatus(isSwapin, txid, pairID, bind, mongodb.SwapInBlacklist, now(), err.Error())
		return nil
	}

	err = preventReswap(res, isSwapin)
	if err != nil {
		return err
	}

	logWorker("swap", "start process swap", "pairID", pairID, "txid", txid, "bind", bind, "status", swap.Status, "isSwapin", isSwapin, "value", res.Value)

	srcBridge := tokens.GetCrossChainBridge(isSwapin)
	swapInfo, err := verifySwapTransaction(srcBridge, pairID, txid, bind, tokens.SwapTxType(swap.TxType))
	if err != nil {
		return fmt.Errorf("[doSwap] reverify swap failed, %w", err)
	}
	if swapInfo.Value.String() != res.Value {
		return fmt.Errorf("[doSwap] reverify swap value mismatch, in db %v != %v", res.Value, swapInfo.Value)
	}
	if !strings.EqualFold(swapInfo.Bind, bind) {
		return fmt.Errorf("[doSwap] reverify swap bind address mismatch, in db %v != %v", bind, swapInfo.Bind)
	}

	swapType := getSwapType(isSwapin)
	args := &tokens.BuildTxArgs{
		SwapInfo: tokens.SwapInfo{
			Identifier: params.GetIdentifier(),
			PairID:     pairID,
			SwapID:     txid,
			SwapType:   swapType,
			TxType:     tokens.SwapTxType(swap.TxType),
			Bind:       bind,
		},
		From:        toTokenCfg.DcrmAddress,
		OriginFrom:  swap.From,
		OriginTxTo:  swap.TxTo,
		OriginValue: swapInfo.Value,
	}

	return dispatchSwapTask(args)
}

func preventReswap(res *mongodb.MgoSwapResult, isSwapin bool) (err error) {
	err = processNonEmptySwapResult(res, isSwapin)
	if err != nil {
		return err
	}
	return processHistory(res, isSwapin)
}

func getSwapType(isSwapin bool) tokens.SwapType {
	if isSwapin {
		return tokens.SwapinType
	}
	return tokens.SwapoutType
}

func processNonEmptySwapResult(res *mongodb.MgoSwapResult, isSwapin bool) error {
	if res.SwapNonce > 0 || res.SwapTx != "" || res.SwapHeight != 0 || len(res.OldSwapTxs) > 0 {
		_ = mongodb.UpdateSwapStatus(isSwapin, res.TxID, res.PairID, res.Bind, mongodb.TxProcessed, now(), "")
		return errAlreadySwapped
	}
	if res.SwapTx == "" {
		return nil
	}
	txid := res.TxID
	pairID := res.PairID
	bind := res.Bind
	_ = mongodb.UpdateSwapStatus(isSwapin, txid, pairID, bind, mongodb.TxProcessed, now(), "")
	if res.Status != mongodb.MatchTxEmpty {
		return errAlreadySwapped
	}
	resBridge := tokens.GetCrossChainBridge(!isSwapin)
	if _, err := resBridge.GetTransaction(res.SwapTx); err == nil {
		return errAlreadySwapped
	}
	return nil
}

func processHistory(res *mongodb.MgoSwapResult, isSwapin bool) error {
	pairID, txid, bind := res.PairID, res.TxID, res.Bind
	history := getSwapHistory(txid, bind, isSwapin)
	if history == nil {
		return nil
	}
	if res.Status == mongodb.MatchTxFailed || res.Status == mongodb.MatchTxEmpty {
		history.txid = "" // mark ineffective
		return nil
	}
	resBridge := tokens.GetCrossChainBridge(!isSwapin)
	if _, err := resBridge.GetTransaction(history.matchTx); err == nil {
		_ = mongodb.UpdateSwapStatus(isSwapin, res.TxID, res.PairID, res.Bind, mongodb.TxProcessed, now(), "")
		logWorker("swap", "ignore swapped swap", "txid", txid, "pairID", pairID, "bind", bind, "matchTx", history.matchTx, "isSwapin", isSwapin)
		return errAlreadySwapped
	}
	return nil
}

func dispatchSwapTask(args *tokens.BuildTxArgs) error {
	from := strings.ToLower(args.From)
	switch args.SwapType {
	case tokens.SwapinType:
		swapChan, exist := swapinTaskChanMap[from]
		if !exist {
			return fmt.Errorf("no swapin task channel for dcrm address '%v'", args.From)
		}
		swapChan <- args
	case tokens.SwapoutType:
		swapChan, exist := swapoutTaskChanMap[from]
		if !exist {
			return fmt.Errorf("no swapout task channel for dcrm address '%v'", args.From)
		}
		swapChan <- args
	default:
		return fmt.Errorf("wrong swap type '%v'", args.SwapType.String())
	}
	logWorker("doSwap", "dispatch swap task", "pairID", args.PairID, "txid", args.SwapID, "bind", args.Bind, "swapType", args.SwapType.String(), "value", args.OriginValue)
	return nil
}

func processSwapTask(swapChan <-chan *tokens.BuildTxArgs) {
	for {
		args := <-swapChan
		err := doSwap(args)
		switch err {
		case nil, errAlreadySwapped:
		default:
			logWorkerError("doSwap", "process failed", err, "pairID", args.PairID, "txid", args.SwapID, "swapType", args.SwapType.String(), "value", args.OriginValue)
		}
	}
}

func getSwapCacheKey(isSwapin bool, txid, bind string) string {
	return strings.ToLower(fmt.Sprintf("%s:%s:%t", txid, bind, isSwapin))
}

func checkAndUpdateProcessSwapTaskCache(key string) error {
	if cachedSwapTasks.Contains(key) {
		return errAlreadySwapped
	}
	if cachedSwapTasks.Cardinality() >= maxCachedSwapTasks {
		cachedSwapTasks.Pop()
	}
	cachedSwapTasks.Add(key)
	return nil
}

func doSwap(args *tokens.BuildTxArgs) (err error) {
	pairID := args.PairID
	txid := args.SwapID
	bind := args.Bind
	swapType := args.SwapType
	originValue := args.OriginValue

	isSwapin := swapType == tokens.SwapinType
	resBridge := tokens.GetCrossChainBridge(!isSwapin)

	cacheKey := getSwapCacheKey(isSwapin, txid, bind)
	err = checkAndUpdateProcessSwapTaskCache(cacheKey)
	if err != nil {
		return err
	}
	logWorker("doSwap", "add swap cache", "pairID", pairID, "txid", txid, "bind", bind, "isSwapin", isSwapin, "value", args.OriginValue)
	isCachedSwapProcessed := false
	defer func() {
		if !isCachedSwapProcessed {
			logWorkerError("doSwap", "delete swap cache", err, "pairID", pairID, "txid", txid, "bind", bind, "isSwapin", isSwapin, "value", args.OriginValue)
			cachedSwapTasks.Remove(cacheKey)
		}
	}()

	logWorker("doSwap", "start to process", "pairID", pairID, "txid", txid, "bind", bind, "isSwapin", isSwapin, "value", originValue)

	rawTx, err := resBridge.BuildRawTransaction(args)
	if err != nil {
		logWorkerError("doSwap", "build tx failed", err, "txid", txid, "bind", bind, "isSwapin", isSwapin)
		return err
	}

	var signedTx interface{}
	var txHash string
	tokenCfg := resBridge.GetTokenConfig(pairID)
	if tokenCfg.GetDcrmAddressPrivateKey() != nil {
		signedTx, txHash, err = resBridge.SignTransaction(rawTx, pairID)
	} else {
		signedTx, txHash, err = resBridge.DcrmSignTransaction(rawTx, args.GetExtraArgs())
	}
	if err != nil {
		logWorkerError("doSwap", "sign tx failed", err, "txid", txid, "bind", bind, "isSwapin", isSwapin)
		return err
	}

	// recheck reswap before update db
	res, err := mongodb.FindSwapResult(isSwapin, txid, pairID, bind)
	if err != nil {
		return err
	}
	err = preventReswap(res, isSwapin)
	if err != nil {
		return err
	}

	swapNonce := args.GetTxNonce()

	// update database before sending transaction
	addSwapHistory(txid, bind, originValue, txHash, swapNonce, isSwapin)
	matchTx := &MatchTx{
		SwapTx:    txHash,
		SwapValue: tokens.CalcSwappedValue(pairID, originValue, isSwapin, res.From, res.TxTo).String(),
		SwapType:  swapType,
		SwapNonce: swapNonce,
	}
	err = updateSwapResult(txid, pairID, bind, matchTx)
	if err != nil {
		logWorkerError("doSwap", "update swap result failed", err, "txid", txid, "bind", bind, "isSwapin", isSwapin)
		return err
	}
	isCachedSwapProcessed = true

	err = mongodb.UpdateSwapStatus(isSwapin, txid, pairID, bind, mongodb.TxProcessed, now(), "")
	if err != nil {
		logWorkerError("doSwap", "update swap status failed", err, "txid", txid, "bind", bind, "isSwapin", isSwapin)
		return err
	}

	sentTxHash, err := sendSignedTransaction(resBridge, signedTx, args)
	if err == nil {
		logWorker("doSwap", "send tx success", "pairID", pairID, "txid", txid, "bind", bind, "isSwapin", isSwapin, "swapNonce", swapNonce, "txHash", txHash)
		if txHash != sentTxHash {
			logWorkerError("doSwap", "send tx success but with different hash", errSendTxWithDiffHash, "pairID", pairID, "txid", txid, "bind", bind, "isSwapin", isSwapin, "swapNonce", swapNonce, "txHash", txHash, "sentTxHash", sentTxHash)
			_ = replaceSwapResult(txid, pairID, bind, sentTxHash, isSwapin)
		}
	}
	return err
}

// DeleteCachedSwap delete cached swap
func DeleteCachedSwap(isSwapin bool, txid, bind string) {
	cacheKey := getSwapCacheKey(isSwapin, txid, bind)
	cachedSwapTasks.Remove(cacheKey)
}

type swapInfo struct {
	txid     string
	bind     string
	value    *big.Int
	matchTx  string
	nonce    uint64
	isSwapin bool
}

func addSwapHistory(txid, bind string, value *big.Int, matchTx string, nonce uint64, isSwapin bool) {
	// Create the new item as its own ring
	item := ring.New(1)
	item.Value = &swapInfo{
		txid:     txid,
		bind:     bind,
		value:    value,
		matchTx:  matchTx,
		nonce:    nonce,
		isSwapin: isSwapin,
	}

	swapRingLock.Lock()
	defer swapRingLock.Unlock()

	if swapRing == nil {
		swapRing = item
	} else {
		if swapRing.Len() == swapRingMaxSize {
			swapRing = swapRing.Move(-1)
			swapRing.Unlink(1)
			swapRing = swapRing.Move(1)
		}
		swapRing.Move(-1).Link(item)
	}
}

func getSwapHistory(txid, bind string, isSwapin bool) *swapInfo {
	swapRingLock.RLock()
	defer swapRingLock.RUnlock()

	if swapRing == nil {
		return nil
	}

	r := swapRing
	for i := 0; i < r.Len(); i++ {
		item := r.Value.(*swapInfo)
		if item.txid == txid && item.bind == bind && item.isSwapin == isSwapin {
			return item
		}
		r = r.Prev()
	}

	return nil
}
