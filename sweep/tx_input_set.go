package sweep

import (
	"fmt"
	"math"
	"sort"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/fn"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/lnwallet"
	"github.com/lightningnetwork/lnd/lnwallet/chainfee"
)

// addConstraints defines the constraints to apply when adding an input.
type addConstraints uint8

const (
	// constraintsRegular is for regular input sweeps that should have a positive
	// yield.
	constraintsRegular addConstraints = iota

	// constraintsWallet is for wallet inputs that are only added to bring up the tx
	// output value.
	constraintsWallet

	// constraintsForce is for inputs that should be swept even with a negative
	// yield at the set fee rate.
	constraintsForce
)

var (
	// ErrNotEnoughInputs is returned when there are not enough wallet
	// inputs to construct a non-dust change output for an input set.
	ErrNotEnoughInputs = fmt.Errorf("not enough inputs")

	// ErrDeadlinesMismatch is returned when the deadlines of the input
	// sets do not match.
	ErrDeadlinesMismatch = fmt.Errorf("deadlines mismatch")

	// ErrDustOutput is returned when the output value is below the dust
	// limit.
	ErrDustOutput = fmt.Errorf("dust output")
)

// InputSet defines an interface that's responsible for filtering a set of
// inputs that can be swept economically.
type InputSet interface {
	// Inputs returns the set of inputs that should be used to create a tx.
	Inputs() []input.Input

	// AddWalletInputs adds wallet inputs to the set until a non-dust
	// change output can be made. Return an error if there are not enough
	// wallet inputs.
	AddWalletInputs(wallet Wallet) error

	// NeedWalletInput returns true if the input set needs more wallet
	// inputs.
	NeedWalletInput() bool

	// DeadlineHeight returns an absolute block height to express the
	// time-sensitivity of the input set. The outputs from a force close tx
	// have different time preferences:
	// - to_local: no time pressure as it can only be swept by us.
	// - first level outgoing HTLC: must be swept before its corresponding
	//   incoming HTLC's CLTV is reached.
	// - first level incoming HTLC: must be swept before its CLTV is
	//   reached.
	// - second level HTLCs: no time pressure.
	// - anchor: for CPFP-purpose anchor, it must be swept before any of
	//   the above CLTVs is reached. For non-CPFP purpose anchor, there's
	//   no time pressure.
	DeadlineHeight() int32

	// Budget givens the total amount that can be used as fees by this
	// input set.
	Budget() btcutil.Amount

	// StartingFeeRate returns the max starting fee rate found in the
	// inputs.
	StartingFeeRate() fn.Option[chainfee.SatPerKWeight]
}

type txInputSetState struct {
	// feeRate is the fee rate to use for the sweep transaction.
	feeRate chainfee.SatPerKWeight

	// maxFeeRate is the max allowed fee rate configured by the user.
	maxFeeRate chainfee.SatPerKWeight

	// inputTotal is the total value of all inputs.
	inputTotal btcutil.Amount

	// requiredOutput is the sum of the outputs committed to by the inputs.
	requiredOutput btcutil.Amount

	// changeOutput is the value of the change output. This will be what is
	// left over after subtracting the requiredOutput and the tx fee from
	// the inputTotal.
	//
	// NOTE: This might be below the dust limit, or even negative since it
	// is the change remaining in csse we pay the fee for a change output.
	changeOutput btcutil.Amount

	// inputs is the set of tx inputs.
	inputs []input.Input

	// walletInputTotal is the total value of inputs coming from the wallet.
	walletInputTotal btcutil.Amount

	// force indicates that this set must be swept even if the total yield
	// is negative.
	force bool
}

// weightEstimate is the (worst case) tx weight with the current set of
// inputs. It takes a parameter whether to add a change output or not.
func (t *txInputSetState) weightEstimate(change bool) *weightEstimator {
	weightEstimate := newWeightEstimator(t.feeRate, t.maxFeeRate)
	for _, i := range t.inputs {
		// Can ignore error, because it has already been checked when
		// calculating the yields.
		_ = weightEstimate.add(i)

		r := i.RequiredTxOut()
		if r != nil {
			weightEstimate.addOutput(r)
		}
	}

	// Add a change output to the weight estimate if requested.
	if change {
		weightEstimate.addP2TROutput()
	}

	return weightEstimate
}

// totalOutput is the total amount left for us after paying fees.
//
// NOTE: This might be dust.
func (t *txInputSetState) totalOutput() btcutil.Amount {
	return t.requiredOutput + t.changeOutput
}

func (t *txInputSetState) clone() txInputSetState {
	s := txInputSetState{
		feeRate:          t.feeRate,
		inputTotal:       t.inputTotal,
		changeOutput:     t.changeOutput,
		requiredOutput:   t.requiredOutput,
		walletInputTotal: t.walletInputTotal,
		force:            t.force,
		inputs:           make([]input.Input, len(t.inputs)),
	}
	copy(s.inputs, t.inputs)

	return s
}

// txInputSet is an object that accumulates tx inputs and keeps running counters
// on various properties of the tx.
type txInputSet struct {
	txInputSetState

	// maxInputs is the maximum number of inputs that will be accepted in
	// the set.
	maxInputs uint32
}

// Compile-time constraint to ensure txInputSet implements InputSet.
var _ InputSet = (*txInputSet)(nil)

// newTxInputSet constructs a new, empty input set.
func newTxInputSet(feePerKW, maxFeeRate chainfee.SatPerKWeight,
	maxInputs uint32) *txInputSet {

	state := txInputSetState{
		feeRate:    feePerKW,
		maxFeeRate: maxFeeRate,
	}

	b := txInputSet{
		maxInputs:       maxInputs,
		txInputSetState: state,
	}

	return &b
}

// Inputs returns the inputs that should be used to create a tx.
func (t *txInputSet) Inputs() []input.Input {
	return t.inputs
}

// Budget gives the total amount that can be used as fees by this input set.
//
// NOTE: this field is only used for `BudgetInputSet`.
func (t *txInputSet) Budget() btcutil.Amount {
	return t.totalOutput()
}

// DeadlineHeight gives the block height that this set must be confirmed by.
//
// NOTE: this field is only used for `BudgetInputSet`.
func (t *txInputSet) DeadlineHeight() int32 {
	return 0
}

// StartingFeeRate returns the max starting fee rate found in the inputs.
//
// NOTE: this field is only used for `BudgetInputSet`.
func (t *txInputSet) StartingFeeRate() fn.Option[chainfee.SatPerKWeight] {
	return fn.None[chainfee.SatPerKWeight]()
}

// NeedWalletInput returns true if the input set needs more wallet inputs.
func (t *txInputSet) NeedWalletInput() bool {
	return !t.enoughInput()
}

// enoughInput returns true if we've accumulated enough inputs to pay the fees
// and have at least one output that meets the dust limit.
func (t *txInputSet) enoughInput() bool {
	// If we have a change output above dust, then we certainly have enough
	// inputs to the transaction.
	if t.changeOutput >= lnwallet.DustLimitForSize(input.P2TRSize) {
		return true
	}

	// We did not have enough input for a change output. Check if we have
	// enough input to pay the fees for a transaction with no change
	// output.
	fee := t.weightEstimate(false).feeWithParent()
	if t.inputTotal < t.requiredOutput+fee {
		return false
	}

	// We could pay the fees, but we still need at least one output to be
	// above the dust limit for the tx to be valid (we assume that these
	// required outputs only get added if they are above dust)
	for _, inp := range t.inputs {
		if inp.RequiredTxOut() != nil {
			return true
		}
	}

	return false
}

// add adds a new input to the set. It returns a bool indicating whether the
// input was added to the set. An input is rejected if it decreases the tx
// output value after paying fees.
func (t *txInputSet) addToState(inp input.Input,
	constraints addConstraints) *txInputSetState {

	// Stop if max inputs is reached. Do not count additional wallet inputs,
	// because we don't know in advance how many we may need.
	if constraints != constraintsWallet &&
		uint32(len(t.inputs)) >= t.maxInputs {

		return nil
	}

	// If the input comes with a required tx out that is below dust, we
	// won't add it.
	//
	// NOTE: only HtlcSecondLevelAnchorInput returns non-nil RequiredTxOut.
	reqOut := inp.RequiredTxOut()
	if reqOut != nil {
		// Fetch the dust limit for this output.
		dustLimit := lnwallet.DustLimitForSize(len(reqOut.PkScript))
		if btcutil.Amount(reqOut.Value) < dustLimit {
			log.Errorf("Rejected input=%v due to dust required "+
				"output=%v, limit=%v", inp, reqOut.Value,
				dustLimit)

			// TODO(yy): we should not return here for force
			// sweeps. This means when sending sweeping request,
			// one must be careful to not create dust outputs. In
			// an extreme rare case, where the
			// minRelayTxFee/discardfee is increased when sending
			// the request, what's considered non-dust at the
			// caller side will be dust here, causing a force sweep
			// to fail.
			return nil
		}
	}

	// Clone the current set state.
	newSet := t.clone()

	// Add the new input.
	newSet.inputs = append(newSet.inputs, inp)

	// Add the value of the new input.
	value := btcutil.Amount(inp.SignDesc().Output.Value)
	newSet.inputTotal += value

	// Recalculate the tx fee.
	fee := newSet.weightEstimate(true).feeWithParent()

	// Calculate the new output value.
	if reqOut != nil {
		newSet.requiredOutput += btcutil.Amount(reqOut.Value)
	}

	// NOTE: `changeOutput` could be negative here if this input is using
	// constraintsForce.
	newSet.changeOutput = newSet.inputTotal - newSet.requiredOutput - fee

	// Calculate the yield of this input from the change in total tx output
	// value.
	inputYield := newSet.totalOutput() - t.totalOutput()

	switch constraints {
	// Don't sweep inputs that cost us more to sweep than they give us.
	case constraintsRegular:
		if inputYield <= 0 {
			log.Debugf("Rejected regular input=%v due to negative "+
				"yield=%v", value, inputYield)

			return nil
		}

	// For force adds, no further constraints apply.
	//
	// NOTE: because the inputs are sorted with force sweeps being placed
	// at the start of the list, we should never see an input with
	// constraintsForce come after an input with constraintsRegular. In
	// other words, though we may have negative `changeOutput` from
	// including force sweeps, `inputYield` should always increase when
	// adding regular inputs.
	case constraintsForce:
		newSet.force = true

	// We are attaching a wallet input to raise the tx output value above
	// the dust limit.
	case constraintsWallet:
		// Skip this wallet input if adding it would lower the output
		// value.
		//
		// TODO(yy): change to inputYield < 0 to allow sweeping for
		// UTXO aggregation only?
		if inputYield <= 0 {
			log.Debugf("Rejected wallet input=%v due to negative "+
				"yield=%v", value, inputYield)

			return nil
		}

		// Calculate the total value that we spend in this tx from the
		// wallet if we'd add this wallet input.
		newSet.walletInputTotal += value

		// In any case, we don't want to lose money by sweeping. If we
		// don't get more out of the tx than we put in ourselves, do not
		// add this wallet input. If there is at least one force sweep
		// in the set, this does no longer apply.
		//
		// We should only add wallet inputs to get the tx output value
		// above the dust limit, otherwise we'd only burn into fees.
		// This is guarded by tryAddWalletInputsIfNeeded.
		//
		// TODO(joostjager): Possibly require a max ratio between the
		// value of the wallet input and what we get out of this
		// transaction. To prevent attaching and locking a big utxo for
		// very little benefit.
		if newSet.force {
			break
		}

		// TODO(yy): change from `>=` to `>` to allow non-negative
		// sweeping - we won't gain more coins from this sweep, but
		// aggregating small UTXOs.
		if newSet.walletInputTotal >= newSet.totalOutput() {
			// TODO(yy): further check this case as it seems we can
			// never reach here because it'd mean `inputYield` is
			// already <= 0?
			log.Debugf("Rejecting wallet input of %v, because it "+
				"would make a negative yielding transaction "+
				"(%v)", value,
				newSet.totalOutput()-newSet.walletInputTotal)

			return nil
		}
	}

	return &newSet
}

// add adds a new input to the set. It returns a bool indicating whether the
// input was added to the set. An input is rejected if it decreases the tx
// output value after paying fees.
func (t *txInputSet) add(input input.Input, constraints addConstraints) bool {
	newState := t.addToState(input, constraints)
	if newState == nil {
		return false
	}

	t.txInputSetState = *newState

	return true
}

// addPositiveYieldInputs adds sweepableInputs that have a positive yield to the
// input set. This function assumes that the list of inputs is sorted descending
// by yield.
//
// TODO(roasbeef): Consider including some negative yield inputs too to clean
// up the utxo set even if it costs us some fees up front.  In the spirit of
// minimizing any negative externalities we cause for the Bitcoin system as a
// whole.
func (t *txInputSet) addPositiveYieldInputs(sweepableInputs []*SweeperInput) {
	for i, inp := range sweepableInputs {
		// Apply relaxed constraints for force sweeps.
		constraints := constraintsRegular
		if inp.parameters().Immediate {
			constraints = constraintsForce
		}

		// Try to add the input to the transaction. If that doesn't
		// succeed because it wouldn't increase the output value,
		// return. Assuming inputs are sorted by yield, any further
		// inputs wouldn't increase the output value either.
		if !t.add(inp, constraints) {
			var rem []input.Input
			for j := i; j < len(sweepableInputs); j++ {
				rem = append(rem, sweepableInputs[j])
			}
			log.Debugf("%d negative yield inputs not added to "+
				"input set: %v", len(rem),
				inputTypeSummary(rem))
			return
		}

		log.Debugf("Added positive yield input %v to input set",
			inputTypeSummary([]input.Input{inp}))
	}

	// We managed to add all inputs to the set.
}

// AddWalletInputs adds wallet inputs to the set until a non-dust output can be
// made. This non-dust output is either a change output or a required output.
// Return an error if there are not enough wallet inputs.
func (t *txInputSet) AddWalletInputs(wallet Wallet) error {
	// Check the current output value and add wallet utxos if needed to
	// push the output value to the lower limit.
	if err := t.tryAddWalletInputsIfNeeded(wallet); err != nil {
		return err
	}

	// If the output value of this block of inputs does not reach the dust
	// limit, stop sweeping. Because of the sorting, continuing with the
	// remaining inputs will only lead to sets with an even lower output
	// value.
	if !t.enoughInput() {
		// The change output is always a p2tr here.
		dl := lnwallet.DustLimitForSize(input.P2TRSize)
		log.Debugf("Input set value %v (required=%v, change=%v) "+
			"below dust limit of %v", t.totalOutput(),
			t.requiredOutput, t.changeOutput, dl)

		return ErrNotEnoughInputs
	}

	return nil
}

// tryAddWalletInputsIfNeeded retrieves utxos from the wallet and tries adding
// as many as required to bring the tx output value above the given minimum.
func (t *txInputSet) tryAddWalletInputsIfNeeded(wallet Wallet) error {
	// If we've already have enough to pay the transaction fees and have at
	// least one output materialize, no action is needed.
	if t.enoughInput() {
		return nil
	}

	// Retrieve wallet utxos. Only consider confirmed utxos to prevent
	// problems around RBF rules for unconfirmed inputs. This currently
	// ignores the configured coin selection strategy.
	utxos, err := wallet.ListUnspentWitnessFromDefaultAccount(
		1, math.MaxInt32,
	)
	if err != nil {
		return err
	}

	// Sort the UTXOs by putting smaller values at the start of the slice
	// to avoid locking large UTXO for sweeping.
	//
	// TODO(yy): add more choices to CoinSelectionStrategy and use the
	// configured value here.
	sort.Slice(utxos, func(i, j int) bool {
		return utxos[i].Value < utxos[j].Value
	})

	for _, utxo := range utxos {
		input, err := createWalletTxInput(utxo)
		if err != nil {
			return err
		}

		// If the wallet input isn't positively-yielding at this fee
		// rate, skip it.
		if !t.add(input, constraintsWallet) {
			continue
		}

		// Return if we've reached the minimum output amount.
		if t.enoughInput() {
			return nil
		}
	}

	// We were not able to reach the minimum output amount.
	return nil
}

// createWalletTxInput converts a wallet utxo into an object that can be added
// to the other inputs to sweep.
func createWalletTxInput(utxo *lnwallet.Utxo) (input.Input, error) {
	signDesc := &input.SignDescriptor{
		Output: &wire.TxOut{
			PkScript: utxo.PkScript,
			Value:    int64(utxo.Value),
		},
		HashType: txscript.SigHashAll,
	}

	var witnessType input.WitnessType
	switch utxo.AddressType {
	case lnwallet.WitnessPubKey:
		witnessType = input.WitnessKeyHash
	case lnwallet.NestedWitnessPubKey:
		witnessType = input.NestedWitnessKeyHash
	case lnwallet.TaprootPubkey:
		witnessType = input.TaprootPubKeySpend
		signDesc.HashType = txscript.SigHashDefault
	default:
		return nil, fmt.Errorf("unknown address type %v",
			utxo.AddressType)
	}

	// A height hint doesn't need to be set, because we don't monitor these
	// inputs for spend.
	heightHint := uint32(0)

	return input.NewBaseInput(
		&utxo.OutPoint, witnessType, signDesc, heightHint,
	), nil
}

// BudgetInputSet implements the interface `InputSet`. It takes a list of
// pending inputs which share the same deadline height and groups them into a
// set conditionally based on their economical values.
type BudgetInputSet struct {
	// inputs is the set of inputs that have been added to the set after
	// considering their economical contribution.
	inputs []*SweeperInput

	// deadlineHeight is the height which the inputs in this set must be
	// confirmed by.
	deadlineHeight int32
}

// Compile-time constraint to ensure budgetInputSet implements InputSet.
var _ InputSet = (*BudgetInputSet)(nil)

// validateInputs is used when creating new BudgetInputSet to ensure there are
// no duplicate inputs and they all share the same deadline heights, if set.
func validateInputs(inputs []SweeperInput, deadlineHeight int32) error {
	// Sanity check the input slice to ensure it's non-empty.
	if len(inputs) == 0 {
		return fmt.Errorf("inputs slice is empty")
	}

	// inputDeadline tracks the input's deadline height. It will be updated
	// if the input has a different deadline than the specified
	// deadlineHeight.
	inputDeadline := deadlineHeight

	// dedupInputs is a set used to track unique outpoints of the inputs.
	dedupInputs := fn.NewSet(
		// Iterate all the inputs and map the function.
		fn.Map(func(inp SweeperInput) wire.OutPoint {
			// If the input has a deadline height, we'll check if
			// it's the same as the specified.
			inp.params.DeadlineHeight.WhenSome(func(h int32) {
				// Exit early if the deadlines matched.
				if h == deadlineHeight {
					return
				}

				// Update the deadline height if it's
				// different.
				inputDeadline = h
			})

			return inp.OutPoint()
		}, inputs)...,
	)

	// Make sure the inputs share the same deadline height when there is
	// one.
	if inputDeadline != deadlineHeight {
		return fmt.Errorf("input deadline height not matched: want "+
			"%d, got %d", deadlineHeight, inputDeadline)
	}

	// Provide a defensive check to ensure that we don't have any duplicate
	// inputs within the set.
	if len(dedupInputs) != len(inputs) {
		return fmt.Errorf("duplicate inputs")
	}

	return nil
}

// NewBudgetInputSet creates a new BudgetInputSet.
func NewBudgetInputSet(inputs []SweeperInput,
	deadlineHeight int32) (*BudgetInputSet, error) {

	// Validate the supplied inputs.
	if err := validateInputs(inputs, deadlineHeight); err != nil {
		return nil, err
	}

	bi := &BudgetInputSet{
		deadlineHeight: deadlineHeight,
		inputs:         make([]*SweeperInput, 0, len(inputs)),
	}

	for _, input := range inputs {
		bi.addInput(input)
	}

	log.Tracef("Created %v", bi.String())

	return bi, nil
}

// String returns a human-readable description of the input set.
func (b *BudgetInputSet) String() string {
	inputsDesc := ""
	for _, input := range b.inputs {
		inputsDesc += fmt.Sprintf("\n%v", input)
	}

	return fmt.Sprintf("BudgetInputSet(budget=%v, deadline=%v, "+
		"inputs=[%v])", b.Budget(), b.DeadlineHeight(), inputsDesc)
}

// addInput adds an input to the input set.
func (b *BudgetInputSet) addInput(input SweeperInput) {
	b.inputs = append(b.inputs, &input)
}

// NeedWalletInput returns true if the input set needs more wallet inputs.
//
// A set may need wallet inputs when it has a required output or its total
// value cannot cover its total budget.
func (b *BudgetInputSet) NeedWalletInput() bool {
	var (
		// budgetNeeded is the amount that needs to be covered from
		// other inputs.
		budgetNeeded btcutil.Amount

		// budgetBorrowable is the amount that can be borrowed from
		// other inputs.
		budgetBorrowable btcutil.Amount
	)

	for _, inp := range b.inputs {
		// If this input has a required output, we can assume it's a
		// second-level htlc txns input. Although this input must have
		// a value that can cover its budget, it cannot be used to pay
		// fees. Instead, we need to borrow budget from other inputs to
		// make the sweep happen. Once swept, the input value will be
		// credited to the wallet.
		if inp.RequiredTxOut() != nil {
			budgetNeeded += inp.params.Budget
			continue
		}

		// Get the amount left after covering the input's own budget.
		// This amount can then be lent to the above input.
		budget := inp.params.Budget
		output := btcutil.Amount(inp.SignDesc().Output.Value)
		budgetBorrowable += output - budget

		// If the input's budget is not even covered by itself, we need
		// to borrow outputs from other inputs.
		if budgetBorrowable < 0 {
			log.Debugf("Input %v specified a budget that exceeds "+
				"its output value: %v > %v", inp, budget,
				output)
		}
	}

	log.Tracef("NeedWalletInput: budgetNeeded=%v, budgetBorrowable=%v",
		budgetNeeded, budgetBorrowable)

	// If we don't have enough extra budget to borrow, we need wallet
	// inputs.
	return budgetBorrowable < budgetNeeded
}

// copyInputs returns a copy of the slice of the inputs in the set.
func (b *BudgetInputSet) copyInputs() []*SweeperInput {
	inputs := make([]*SweeperInput, len(b.inputs))
	copy(inputs, b.inputs)
	return inputs
}

// AddWalletInputs adds wallet inputs to the set until the specified budget is
// met. When sweeping inputs with required outputs, although there's budget
// specified, it cannot be directly spent from these required outputs. Instead,
// we need to borrow budget from other inputs to make the sweep happen.
// There are two sources to borrow from: 1) other inputs, 2) wallet utxos. If
// we are calling this method, it means other inputs cannot cover the specified
// budget, so we need to borrow from wallet utxos.
//
// Return an error if there are not enough wallet inputs, and the budget set is
// set to its initial state by removing any wallet inputs added.
//
// NOTE: must be called with the wallet lock held via `WithCoinSelectLock`.
func (b *BudgetInputSet) AddWalletInputs(wallet Wallet) error {
	// Retrieve wallet utxos. Only consider confirmed utxos to prevent
	// problems around RBF rules for unconfirmed inputs. This currently
	// ignores the configured coin selection strategy.
	utxos, err := wallet.ListUnspentWitnessFromDefaultAccount(
		1, math.MaxInt32,
	)
	if err != nil {
		return fmt.Errorf("list unspent witness: %w", err)
	}

	// Sort the UTXOs by putting smaller values at the start of the slice
	// to avoid locking large UTXO for sweeping.
	//
	// TODO(yy): add more choices to CoinSelectionStrategy and use the
	// configured value here.
	sort.Slice(utxos, func(i, j int) bool {
		return utxos[i].Value < utxos[j].Value
	})

	// Make a copy of the current inputs. If the wallet doesn't have enough
	// utxos to cover the budget, we will revert the current set to its
	// original state by removing the added wallet inputs.
	originalInputs := b.copyInputs()

	// Add wallet inputs to the set until the specified budget is covered.
	for _, utxo := range utxos {
		input, err := createWalletTxInput(utxo)
		if err != nil {
			return err
		}

		pi := SweeperInput{
			Input: input,
			params: Params{
				DeadlineHeight: fn.Some(b.deadlineHeight),
			},
		}
		b.addInput(pi)

		// Return if we've reached the minimum output amount.
		if !b.NeedWalletInput() {
			return nil
		}
	}

	// The wallet doesn't have enough utxos to cover the budget. Revert the
	// input set to its original state.
	b.inputs = originalInputs

	return ErrNotEnoughInputs
}

// Budget returns the total budget of the set.
//
// NOTE: part of the InputSet interface.
func (b *BudgetInputSet) Budget() btcutil.Amount {
	budget := btcutil.Amount(0)
	for _, input := range b.inputs {
		budget += input.params.Budget
	}

	return budget
}

// DeadlineHeight returns the deadline height of the set.
//
// NOTE: part of the InputSet interface.
func (b *BudgetInputSet) DeadlineHeight() int32 {
	return b.deadlineHeight
}

// Inputs returns the inputs that should be used to create a tx.
//
// NOTE: part of the InputSet interface.
func (b *BudgetInputSet) Inputs() []input.Input {
	inputs := make([]input.Input, 0, len(b.inputs))
	for _, inp := range b.inputs {
		inputs = append(inputs, inp.Input)
	}

	return inputs
}

// StartingFeeRate returns the max starting fee rate found in the inputs.
//
// NOTE: part of the InputSet interface.
func (b *BudgetInputSet) StartingFeeRate() fn.Option[chainfee.SatPerKWeight] {
	maxFeeRate := chainfee.SatPerKWeight(0)
	startingFeeRate := fn.None[chainfee.SatPerKWeight]()

	for _, inp := range b.inputs {
		feerate := inp.params.StartingFeeRate.UnwrapOr(0)
		if feerate > maxFeeRate {
			maxFeeRate = feerate
			startingFeeRate = fn.Some(maxFeeRate)
		}
	}

	return startingFeeRate
}
