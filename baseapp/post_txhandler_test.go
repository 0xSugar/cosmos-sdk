package baseapp_test

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/crypto/tmhash"
)

type handlerFun func(ctx sdk.Context, tx sdk.Tx, simulate bool) (newCtx sdk.Context, err error)

type postTxHandler struct {
	handler handlerFun
	inner   tx.Handler
}

var _ tx.Handler = postTxHandler{}

// PostTxHandlerMiddleware is being used in tests for testing post execution of txHandler middlewares.
func PostTxHandlerMiddleware(handler handlerFun) tx.Middleware {
	return func(txHandler tx.Handler) tx.Handler {
		return postTxHandler{
			handler: handler,
			inner:   txHandler,
		}
	}
}

// CheckTx implements tx.Handler.CheckTx method.
func (txh postTxHandler) CheckTx(ctx context.Context, tx sdk.Tx, req abci.RequestCheckTx) (abci.ResponseCheckTx, error) {
	sdkCtx, err := txh.runHandler(ctx, tx, req.Tx, false)
	if err != nil {
		return abci.ResponseCheckTx{}, err
	}

	return txh.inner.CheckTx(sdk.WrapSDKContext(sdkCtx), tx, req)
}

// DeliverTx implements tx.Handler.DeliverTx method.
func (txh postTxHandler) DeliverTx(ctx context.Context, tx sdk.Tx, req abci.RequestDeliverTx) (abci.ResponseDeliverTx, error) {
	sdkCtx, err := txh.runHandler(ctx, tx, req.Tx, false)
	if err != nil {
		return abci.ResponseDeliverTx{}, err
	}

	return txh.inner.DeliverTx(sdk.WrapSDKContext(sdkCtx), tx, req)
}

// SimulateTx implements tx.Handler.SimulateTx method.
func (txh postTxHandler) SimulateTx(ctx context.Context, sdkTx sdk.Tx, req tx.RequestSimulateTx) (tx.ResponseSimulateTx, error) {
	sdkCtx, err := txh.runHandler(ctx, sdkTx, req.TxBytes, true)
	if err != nil {
		return tx.ResponseSimulateTx{}, err
	}

	return txh.inner.SimulateTx(sdk.WrapSDKContext(sdkCtx), sdkTx, req)
}

func (txh postTxHandler) runHandler(ctx context.Context, tx sdk.Tx, txBytes []byte, isSimulate bool) (sdk.Context, error) {
	err := validateBasicTxMsgs(tx.GetMsgs())
	if err != nil {
		return sdk.Context{}, err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if txh.handler == nil {
		return sdkCtx, nil
	}

	ms := sdkCtx.MultiStore()

	// Branch context before Handler call in case it aborts.
	// This is required for both CheckTx and DeliverTx.
	// Ref: https://github.com/cosmos/cosmos-sdk/issues/2772
	//
	// NOTE: Alternatively, we could require that Handler ensures that
	// writes do not happen if aborted/failed.  This may have some
	// performance benefits, but it'll be more difficult to get right.
	cacheCtx, msCache := cacheTxContext(sdkCtx, txBytes)
	cacheCtx = cacheCtx.WithEventManager(sdk.NewEventManager())
	newCtx, err := txh.handler(cacheCtx, tx, isSimulate)
	if err != nil {
		return sdk.Context{}, err
	}

	if !newCtx.IsZero() {
		// At this point, newCtx.MultiStore() is a store branch, or something else
		// replaced by the Handler. We want the original multistore.
		//
		// Also, in the case of the tx aborting, we need to track gas consumed via
		// the instantiated gas meter in the Handler, so we update the context
		// prior to returning.
		sdkCtx = newCtx.WithMultiStore(ms)
	}

	msCache.Write()

	return sdkCtx, nil
}

// cacheTxContext returns a new context based off of the provided context with
// a branched multi-store.
func cacheTxContext(sdkCtx sdk.Context, txBytes []byte) (sdk.Context, sdk.CacheMultiStore) {
	ms := sdkCtx.MultiStore()
	// TODO: https://github.com/cosmos/cosmos-sdk/issues/2824
	msCache := ms.CacheMultiStore()
	if msCache.TracingEnabled() {
		msCache = msCache.SetTracingContext(
			sdk.TraceContext(
				map[string]interface{}{
					"txHash": fmt.Sprintf("%X", tmhash.Sum(txBytes)),
				},
			),
		).(sdk.CacheMultiStore)
	}

	return sdkCtx.WithMultiStore(msCache), msCache
}

// validateBasicTxMsgs executes basic validator calls for messages.
func validateBasicTxMsgs(msgs []sdk.Msg) error {
	if len(msgs) == 0 {
		return sdkerrors.Wrap(sdkerrors.ErrInvalidRequest, "must contain at least one message")
	}

	for _, msg := range msgs {
		err := msg.ValidateBasic()
		if err != nil {
			return err
		}
	}

	return nil
}
