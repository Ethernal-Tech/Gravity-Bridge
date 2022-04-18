package gravity

import (
	"fmt"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Gravity-Bridge/Gravity-Bridge/module/x/gravity/keeper"
	"github.com/Gravity-Bridge/Gravity-Bridge/module/x/gravity/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestValsetCreationIfNotAvailable(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()
	pk := input.GravityKeeper

	// EndBlocker should set a new validator set if not available
	EndBlocker(ctx, pk)
	require.NotNil(t, pk.GetValset(ctx, keeper.EthChainPrefix, uint64(pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix))))
	valsets := pk.GetValsets(ctx, keeper.EthChainPrefix)
	require.True(t, len(valsets) == 1)
}

func TestValsetCreationUponUnbonding(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()
	pk := input.GravityKeeper

	currentValsetNonce := pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix)
	pk.SetValsetRequest(ctx, keeper.EthChainPrefix)

	input.Context = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	// begin unbonding
	sh := staking.NewHandler(input.StakingKeeper)
	undelegateMsg := keeper.NewTestMsgUnDelegateValidator(keeper.ValAddrs[0], keeper.StakingAmount)
	sh(input.Context, undelegateMsg)

	// Run the staking endblocker to ensure valset is set in state
	staking.EndBlocker(input.Context, input.StakingKeeper)
	EndBlocker(input.Context, pk)

	// TODO: Is this the right check to replace blockHeight == latestValsetNonce with?
	assert.NotEqual(t, currentValsetNonce, pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix))
}

func TestValsetSlashing_ValsetCreated_Before_ValidatorBonded(t *testing.T) {
	//	Don't slash validators if valset is created before he is bonded.

	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := input.GravityKeeper.GetParams(ctx)

	vs, err := pk.GetCurrentValset(ctx, keeper.EthChainPrefix)
	require.NoError(t, err)
	height := uint64(ctx.BlockHeight()) - (params.SignedValsetsWindow + 1)
	vs.Height = height
	vs.Nonce = height
	pk.StoreValset(ctx, keeper.EthChainPrefix, vs)

	EndBlocker(ctx, pk)

	// ensure that the  validator who is bonded after valset is created is not slashed
	val := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[0])
	require.False(t, val.IsJailed())
}

func TestValsetSlashing_ValsetCreated_After_ValidatorBonded(t *testing.T) {
	//	Slashing Conditions for Bonded Validator

	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := input.GravityKeeper.GetParams(ctx)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + int64(params.SignedValsetsWindow) + 2)
	vs, err := pk.GetCurrentValset(ctx, keeper.EthChainPrefix)
	require.NoError(t, err)
	height := uint64(ctx.BlockHeight()) - (params.SignedValsetsWindow + 1)
	vs.Height = height

	vs.Nonce = pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix) + 1
	pk.StoreValset(ctx, keeper.EthChainPrefix, vs)
	pk.SetLatestValsetNonce(ctx, keeper.EthChainPrefix, vs.Nonce)

	for i, orch := range keeper.OrchAddrs {
		if i == 0 {
			// don't sign with first validator
			continue
		}
		ethAddr, err := types.NewEthAddress(keeper.EthAddrs[i].String())
		require.NoError(t, err)

		conf := types.NewMsgValsetConfirm(vs.Nonce, *ethAddr, orch, "dummysig")
		pk.SetValsetConfirm(ctx, keeper.EthChainPrefix, *conf)
	}

	EndBlocker(ctx, pk)

	// ensure that the  validator who is bonded before valset is created is slashed
	val := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[0])
	require.True(t, val.IsJailed())

	// ensure that the  validator who attested the valset is not slashed.
	val = input.StakingKeeper.Validator(ctx, keeper.ValAddrs[1])
	require.False(t, val.IsJailed())

}

func TestNonValidatorValsetConfirm(t *testing.T) {
	//	Test if a non-validator confirm won't panic

	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := input.GravityKeeper.GetParams(ctx)

	// Create not nice guy with very little stake
	consPrivKey := ed25519.GenPrivKey()
	consPubKey := consPrivKey.PubKey()
	valPrivKey := secp256k1.GenPrivKey()
	valPubKey := valPrivKey.PubKey()
	valAddr := sdk.ValAddress(valPubKey.Address())
	accAddr := sdk.AccAddress(valPubKey.Address())

	// Initialize the account for the key
	acc := input.AccountKeeper.NewAccount(
		input.Context,
		authtypes.NewBaseAccount(accAddr, valPubKey, 0, 0),
	)

	require.NoError(t, input.BankKeeper.MintCoins(input.Context, types.ModuleName, keeper.InitCoins))
	input.BankKeeper.SendCoinsFromModuleToAccount(
		input.Context,
		types.ModuleName,
		accAddr,
		keeper.InitCoins,
	)

	// Set the account in state
	input.AccountKeeper.SetAccount(input.Context, acc)

	sh := staking.NewHandler(input.StakingKeeper)
	_, err := sh(
		input.Context,
		keeper.NewTestMsgCreateValidator(valAddr, consPubKey, sdk.NewIntFromUint64(1)),
	)
	require.NoError(t, err)
	// Run the staking endblocker to ensure valset is correct in state
	staking.EndBlocker(input.Context, input.StakingKeeper)

	ethAddr, err := types.NewEthAddress("0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B")
	if err != nil {
		panic("found invalid address in EthAddr")
	}
	input.GravityKeeper.SetEvmAddressForValidator(input.Context, valAddr, *ethAddr)
	input.GravityKeeper.SetOrchestratorValidator(input.Context, valAddr, accAddr)

	notNiceVal, found := pk.GetOrchestratorValidator(ctx, accAddr)
	require.True(t, found)
	require.Equal(t, notNiceVal.Status, stakingtypes.Unbonded)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + int64(params.SignedValsetsWindow) + 2)
	vs, err := pk.GetCurrentValset(ctx, keeper.EthChainPrefix)
	require.NoError(t, err)
	height := uint64(ctx.BlockHeight()) - (params.SignedValsetsWindow + 1)
	vs.Height = height

	vs.Nonce = pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix) + 1
	pk.StoreValset(ctx, keeper.EthChainPrefix, vs)
	pk.SetLatestValsetNonce(ctx, keeper.EthChainPrefix, vs.Nonce)

	for i, orch := range keeper.OrchAddrs {
		if i == 0 {
			// don't sign with first validator
			continue
		}
		ethAddr, err := types.NewEthAddress(keeper.EthAddrs[i].String())
		require.NoError(t, err)

		conf := types.NewMsgValsetConfirm(vs.Nonce, *ethAddr, orch, "dummysig")
		pk.SetValsetConfirm(ctx, keeper.EthChainPrefix, *conf)
	}

	conf := types.NewMsgValsetConfirm(vs.Nonce, *ethAddr, accAddr, "dummysig")
	pk.SetValsetConfirm(ctx, keeper.EthChainPrefix, *conf)

	// Now remove all the stake
	_, err = sh(
		input.Context,
		keeper.NewTestMsgUnDelegateValidator(valAddr, sdk.NewIntFromUint64(1)),
	)
	require.NoError(t, err)

	EndBlocker(ctx, pk)
}

func TestValsetSlashing_UnbondingValidator_UnbondWindow_NotExpired(t *testing.T) {
	//	Slashing Conditions for Unbonding Validator

	//  Create 5 validators
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	val := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[0])
	fmt.Println("val1  tokens", val.GetTokens().ToDec())

	pk := input.GravityKeeper
	params := input.GravityKeeper.GetParams(ctx)

	// Define slashing variables
	validatorStartHeight := ctx.BlockHeight()                                                        // 0
	valsetRequestHeight := validatorStartHeight + 1                                                  // 1
	valUnbondingHeight := valsetRequestHeight + 1                                                    // 2
	valsetRequestSlashedAt := valsetRequestHeight + int64(params.SignedValsetsWindow)                // 11
	validatorUnbondingWindowExpiry := valUnbondingHeight + int64(params.UnbondSlashingValsetsWindow) // 17
	currentBlockHeight := valsetRequestSlashedAt + 1                                                 // 12

	assert.True(t, valsetRequestSlashedAt < currentBlockHeight)
	assert.True(t, valsetRequestHeight < validatorUnbondingWindowExpiry)

	// Create Valset request
	ctx = ctx.WithBlockHeight(valsetRequestHeight)
	vs := pk.SetValsetRequest(ctx, keeper.EthChainPrefix)

	// Start Unbonding validators
	// Validator-1  Unbond slash window is not expired. if not attested, slash
	// Validator-2  Unbond slash window is not expired. if attested, don't slash
	input.Context = ctx.WithBlockHeight(valUnbondingHeight)
	sh := staking.NewHandler(input.StakingKeeper)
	undelegateMsg1 := keeper.NewTestMsgUnDelegateValidator(keeper.ValAddrs[0], keeper.StakingAmount)
	sh(input.Context, undelegateMsg1)
	undelegateMsg2 := keeper.NewTestMsgUnDelegateValidator(keeper.ValAddrs[1], keeper.StakingAmount)
	sh(input.Context, undelegateMsg2)

	for i, orch := range keeper.OrchAddrs {
		if i == 0 {
			// don't sign with first validator
			continue
		}
		ethAddr, err := types.NewEthAddress(keeper.EthAddrs[i].String())
		require.NoError(t, err)

		conf := types.NewMsgValsetConfirm(vs.Nonce, *ethAddr, orch, "dummysig")
		pk.SetValsetConfirm(ctx, keeper.EthChainPrefix, *conf)
	}
	staking.EndBlocker(input.Context, input.StakingKeeper)

	ctx = ctx.WithBlockHeight(currentBlockHeight)
	EndBlocker(ctx, pk)

	// Assertions
	val1 := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[0])
	assert.True(t, val1.IsJailed())
	fmt.Println("val1  tokens", val1.GetTokens().ToDec())
	// check if tokens are slashed for val1.

	val2 := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[1])
	assert.True(t, val2.IsJailed())
	fmt.Println("val2  tokens", val2.GetTokens().ToDec())
	// check if tokens shouldn't be slashed for val2.
}

func TestNonValidatorBatchConfirm(t *testing.T) {
	//	Test if a non-validator confirm won't panic

	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := pk.GetParams(ctx)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + int64(params.SignedValsetsWindow) + 2)

	// Create not nice guy with very little stake
	consPrivKey := ed25519.GenPrivKey()
	consPubKey := consPrivKey.PubKey()
	valPrivKey := secp256k1.GenPrivKey()
	valPubKey := valPrivKey.PubKey()
	valAddr := sdk.ValAddress(valPubKey.Address())
	accAddr := sdk.AccAddress(valPubKey.Address())

	// Initialize the account for the key
	acc := input.AccountKeeper.NewAccount(
		input.Context,
		authtypes.NewBaseAccount(accAddr, valPubKey, 0, 0),
	)

	require.NoError(t, input.BankKeeper.MintCoins(input.Context, types.ModuleName, keeper.InitCoins))
	input.BankKeeper.SendCoinsFromModuleToAccount(
		input.Context,
		types.ModuleName,
		accAddr,
		keeper.InitCoins,
	)

	// Set the account in state
	input.AccountKeeper.SetAccount(input.Context, acc)

	sh := staking.NewHandler(input.StakingKeeper)
	_, err := sh(
		input.Context,
		keeper.NewTestMsgCreateValidator(valAddr, consPubKey, sdk.NewIntFromUint64(1)),
	)
	require.NoError(t, err)
	// Run the staking endblocker to ensure valset is correct in state
	staking.EndBlocker(input.Context, input.StakingKeeper)

	ethAddr, err := types.NewEthAddress("0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B")
	if err != nil {
		panic("found invalid address in EthAddr")
	}
	input.GravityKeeper.SetEvmAddressForValidator(input.Context, valAddr, *ethAddr)
	input.GravityKeeper.SetOrchestratorValidator(input.Context, valAddr, accAddr)

	notNiceVal, found := pk.GetOrchestratorValidator(ctx, accAddr)
	require.True(t, found)
	require.Equal(t, notNiceVal.Status, stakingtypes.Unbonded)

	// First store a batch

	batch, err := types.NewInternalOutgingTxBatchFromExternalBatch(types.OutgoingTxBatch{
		BatchNonce:    1,
		BatchTimeout:  0,
		Transactions:  []types.OutgoingTransferTx{},
		TokenContract: keeper.TokenContractAddrs[0],
		Block:         uint64(ctx.BlockHeight() - int64(params.SignedBatchesWindow+1)),
	})
	require.NoError(t, err)
	pk.StoreBatch(ctx, keeper.EthChainPrefix, *batch)
	unslashedBatches := pk.GetUnSlashedBatches(ctx, keeper.EthChainPrefix, uint64(ctx.BlockHeight()))
	assert.True(t, len(unslashedBatches) == 1 && unslashedBatches[0].BatchNonce == 1)

	for i, orch := range keeper.OrchAddrs {
		pk.SetBatchConfirm(ctx, &types.MsgConfirmBatch{
			Nonce:         batch.BatchNonce,
			TokenContract: keeper.TokenContractAddrs[0],
			EthSigner:     keeper.EthAddrs[i].String(),
			Orchestrator:  orch.String(),
			Signature:     "",
		})
	}

	// Sign using our not nice validator
	// This is not really possible if we use confirmHandlerCommon
	pk.SetBatchConfirm(ctx, &types.MsgConfirmBatch{
		Nonce:         batch.BatchNonce,
		TokenContract: keeper.TokenContractAddrs[0],
		EthSigner:     ethAddr.GetAddress().Hex(),
		Orchestrator:  accAddr.String(),
		Signature:     "",
	})

	// Now remove all the stake
	_, err = sh(
		input.Context,
		keeper.NewTestMsgUnDelegateValidator(valAddr, sdk.NewIntFromUint64(1)),
	)
	require.NoError(t, err)

	EndBlocker(ctx, pk)
}

func TestBatchSlashing(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := pk.GetParams(ctx)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + int64(params.SignedValsetsWindow) + 2)

	// First store a batch

	batch, err := types.NewInternalOutgingTxBatchFromExternalBatch(types.OutgoingTxBatch{
		BatchNonce:    1,
		BatchTimeout:  0,
		Transactions:  []types.OutgoingTransferTx{},
		TokenContract: keeper.TokenContractAddrs[0],
		Block:         uint64(ctx.BlockHeight() - int64(params.SignedBatchesWindow+1)),
	})
	require.NoError(t, err)
	pk.StoreBatch(ctx, keeper.EthChainPrefix, *batch)
	unslashedBatches := pk.GetUnSlashedBatches(ctx, keeper.EthChainPrefix, uint64(ctx.BlockHeight()))
	assert.True(t, len(unslashedBatches) == 1 && unslashedBatches[0].BatchNonce == 1)

	for i, orch := range keeper.OrchAddrs {
		if i == 0 {
			// don't sign with first validator
			continue
		}
		if i == 1 {
			// don't sign with 2nd validator. set val bond height > batch block height
			validator := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[i])
			valConsAddr, _ := validator.GetConsAddr()
			valSigningInfo := slashingtypes.ValidatorSigningInfo{
				Address:             "",
				StartHeight:         int64(batch.Block + 1),
				IndexOffset:         0,
				JailedUntil:         time.Time{},
				Tombstoned:          false,
				MissedBlocksCounter: 0,
			}
			input.SlashingKeeper.SetValidatorSigningInfo(ctx, valConsAddr, valSigningInfo)
			continue
		}

		pk.SetBatchConfirm(ctx, &types.MsgConfirmBatch{
			Nonce:         batch.BatchNonce,
			TokenContract: keeper.TokenContractAddrs[0],
			EthSigner:     keeper.EthAddrs[i].String(),
			Orchestrator:  orch.String(),
			Signature:     "",
		})
	}

	EndBlocker(ctx, pk)

	// ensure that the  validator is jailed and slashed
	val := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[0])
	require.True(t, val.IsJailed())

	// ensure that the 2nd  validator is not jailed and slashed
	val2 := input.StakingKeeper.Validator(ctx, keeper.ValAddrs[1])
	require.False(t, val2.IsJailed())

	// Ensure that the last slashed valset nonce is set properly
	lastSlashedBatchBlock := input.GravityKeeper.GetLastSlashedBatchBlock(ctx, keeper.EthChainPrefix)
	assert.Equal(t, lastSlashedBatchBlock, batch.Block)
	assert.True(t, len(pk.GetUnSlashedBatches(ctx, keeper.EthChainPrefix, uint64(ctx.BlockHeight()))) == 0)

}

func TestValsetEmission(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper

	// Store a validator set with a power change as the most recent validator set
	vs, err := pk.GetCurrentValset(ctx, keeper.EthChainPrefix)
	require.NoError(t, err)
	vs.Nonce--
	internalMembers, err := types.BridgeValidators(vs.Members).ToInternal()
	require.NoError(t, err)
	delta := float64(internalMembers.TotalPower()) * 0.05
	vs.Members[0].Power = uint64(float64(vs.Members[0].Power) - delta/2)
	vs.Members[1].Power = uint64(float64(vs.Members[1].Power) + delta/2)
	pk.StoreValset(ctx, keeper.EthChainPrefix, vs)

	// EndBlocker should set a new validator set
	EndBlocker(ctx, pk)
	require.NotNil(t, pk.GetValset(ctx, keeper.EthChainPrefix, uint64(pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix))))
	valsets := pk.GetValsets(ctx, keeper.EthChainPrefix)
	require.True(t, len(valsets) == 2)
}

func TestValsetSetting(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	pk.SetValsetRequest(ctx, keeper.EthChainPrefix)
	valsets := pk.GetValsets(ctx, keeper.EthChainPrefix)
	require.True(t, len(valsets) == 1)
}

/// Test batch timeout
func TestBatchTimeout(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := pk.GetParams(ctx)
	var (
		now                 = time.Now().UTC()
		mySender, _         = sdk.AccAddressFromBech32("gravity1ahx7f8wyertuus9r20284ej0asrs085ceqtfnm")
		myReceiver          = "0xd041c41EA1bf0F006ADBb6d2c9ef9D425dE5eaD7"
		myTokenContractAddr = "0x429881672B9AE42b8EbA0E26cD9C73711b891Ca5" // Pickle
		token, err          = types.NewInternalERC20Token(sdk.NewInt(99999), myTokenContractAddr)
		allVouchers         = sdk.NewCoins(token.GravityCoin())
	)
	require.NoError(t, err)
	receiver, err := types.NewEthAddress(myReceiver)
	require.NoError(t, err)
	tokenContract, err := types.NewEthAddress(myTokenContractAddr)
	require.NoError(t, err)

	require.Greater(t, params.AverageBlockTime, uint64(0))
	require.Greater(t, params.AverageEthereumBlockTime, uint64(0))

	// mint some vouchers first
	require.NoError(t, input.BankKeeper.MintCoins(ctx, types.ModuleName, allVouchers))
	// set senders balance
	input.AccountKeeper.NewAccountWithAddress(ctx, mySender)
	require.NoError(t, input.BankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, mySender, allVouchers))

	// add some TX to the pool
	for i, v := range []uint64{4, 3, 3, 4, 5, 6} {
		amountToken, err := types.NewInternalERC20Token(sdk.NewInt(int64(i+100)), myTokenContractAddr)
		require.NoError(t, err)
		amount := amountToken.GravityCoin()
		feeToken, err := types.NewInternalERC20Token(sdk.NewIntFromUint64(v), myTokenContractAddr)
		require.NoError(t, err)
		fee := feeToken.GravityCoin()

		_, err = input.GravityKeeper.AddToOutgoingPool(ctx, keeper.EthChainPrefix, mySender, *receiver, amount, fee)
		require.NoError(t, err)
	}

	// when
	ctx = ctx.WithBlockTime(now)
	ctx = ctx.WithBlockHeight(250)

	// check that we can make a batch without first setting an ethereum block height
	b1, err1 := pk.BuildOutgoingTXBatch(ctx, keeper.EthChainPrefix, *tokenContract, 1)
	require.NoError(t, err1)
	require.Equal(t, b1.BatchTimeout, uint64(0))

	pk.SetLastObservedEvmChainBlockHeight(ctx, keeper.EthChainPrefix, 500)

	// increase number of max txs to create more profitable batch
	b2, err2 := pk.BuildOutgoingTXBatch(ctx, keeper.EthChainPrefix, *tokenContract, 2)
	require.NoError(t, err2)
	// this is exactly block 500 plus twelve hours
	require.Equal(t, b2.BatchTimeout, uint64(504))

	// make sure the batches got stored in the first place
	gotFirstBatch := input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b1.TokenContract, b1.BatchNonce)
	require.NotNil(t, gotFirstBatch)
	gotSecondBatch := input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b2.TokenContract, b2.BatchNonce)
	require.NotNil(t, gotSecondBatch)

	// persist confirmations for second batch to test their deletion on batch timeout
	for i, orch := range keeper.OrchAddrs {
		ethAddr, err := types.NewEthAddress(keeper.EthAddrs[i].String())
		require.NoError(t, err)

		conf := &types.MsgConfirmBatch{
			Nonce:         b2.BatchNonce,
			TokenContract: b2.TokenContract.GetAddress().Hex(),
			EthSigner:     ethAddr.GetAddress().Hex(),
			Orchestrator:  orch.String(),
			Signature:     "dummysig",
		}

		input.GravityKeeper.SetBatchConfirm(ctx, conf)
	}

	// verify that confirms are persisted
	secondBatchConfirms := input.GravityKeeper.GetBatchConfirmByNonceAndTokenContract(ctx, keeper.EthChainPrefix, b2.BatchNonce, b2.TokenContract)
	require.Equal(t, len(keeper.OrchAddrs), len(secondBatchConfirms))

	// when, way into the future
	ctx = ctx.WithBlockTime(now)
	ctx = ctx.WithBlockHeight(9)

	b3, err2 := pk.BuildOutgoingTXBatch(ctx, keeper.EthChainPrefix, *tokenContract, 3)
	require.NoError(t, err2)

	EndBlocker(ctx, pk)

	// this had a timeout of zero should be deleted.
	gotFirstBatch = input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b1.TokenContract, b1.BatchNonce)
	require.Nil(t, gotFirstBatch)
	// make sure the end blocker does not delete these, as the block height has not officially
	// been updated by a relay event
	gotSecondBatch = input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b2.TokenContract, b2.BatchNonce)
	require.NotNil(t, gotSecondBatch)
	gotThirdBatch := input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b3.TokenContract, b3.BatchNonce)
	require.NotNil(t, gotThirdBatch)

	pk.SetLastObservedEvmChainBlockHeight(ctx, keeper.EthChainPrefix, 5000)
	EndBlocker(ctx, pk)

	// make sure the end blocker does delete these, as we've got a new Ethereum block height
	gotFirstBatch = input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b1.TokenContract, b1.BatchNonce)
	require.Nil(t, gotFirstBatch)
	gotSecondBatch = input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b2.TokenContract, b2.BatchNonce)
	require.Nil(t, gotSecondBatch)
	gotThirdBatch = input.GravityKeeper.GetOutgoingTXBatch(ctx, keeper.EthChainPrefix, b3.TokenContract, b3.BatchNonce)
	require.NotNil(t, gotThirdBatch)

	// verify that second batch confirms are deleted
	secondBatchConfirms = input.GravityKeeper.GetBatchConfirmByNonceAndTokenContract(ctx, keeper.EthChainPrefix, b2.BatchNonce, b2.TokenContract)
	require.Equal(t, 0, len(secondBatchConfirms))
}

func TestValsetPruning(t *testing.T) {
	input, ctx := keeper.SetupFiveValChain(t)
	defer func() { input.Context.Logger().Info("Asserting invariants at test end"); input.AssertInvariants() }()

	pk := input.GravityKeeper
	params := pk.GetParams(ctx)

	// Create new validator set with nonce 1
	pk.SetValsetRequest(ctx, keeper.EthChainPrefix)
	firstValsetNonce := pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix)
	require.NotNil(t, pk.GetValset(ctx, keeper.EthChainPrefix, firstValsetNonce))
	require.True(t, len(pk.GetValsets(ctx, keeper.EthChainPrefix)) == 1)

	// Create validator set confirmations
	for i, orch := range keeper.OrchAddrs {
		ethAddr, err := types.NewEthAddress(keeper.EthAddrs[i].String())
		require.NoError(t, err)

		conf := types.NewMsgValsetConfirm(firstValsetNonce, *ethAddr, orch, "dummysig")
		pk.SetValsetConfirm(ctx, keeper.EthChainPrefix, *conf)
	}

	require.True(t, len(pk.GetValsetConfirms(ctx, keeper.EthChainPrefix, firstValsetNonce)) == len(keeper.OrchAddrs))

	// Create new validator set with nonce 2
	pk.SetValsetRequest(ctx, keeper.EthChainPrefix)
	require.True(t, len(pk.GetValsets(ctx, keeper.EthChainPrefix)) == 2)
	valset := pk.GetValset(ctx, keeper.EthChainPrefix, pk.GetLatestValsetNonce(ctx, keeper.EthChainPrefix))
	require.NotNil(t, valset)

	// Set validator set with nonce 2 as last observed
	pk.SetLastObservedValset(ctx, keeper.EthChainPrefix, *valset)
	require.Equal(t, valset.Nonce, pk.GetLastObservedValset(ctx, keeper.EthChainPrefix).Nonce)

	// Advance enough blocks so that old validator set gets removed in EndBlocker
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + int64(params.SignedValsetsWindow+1)).WithBlockTime(time.Now().UTC())

	// EndBlocker should cleanup validator set with nonce 1 and it's confirmations
	EndBlocker(ctx, pk)
	require.Nil(t, pk.GetValset(ctx, keeper.EthChainPrefix, firstValsetNonce))
	require.Equal(t, 0, len(pk.GetValsetConfirms(ctx, keeper.EthChainPrefix, firstValsetNonce)))
}
