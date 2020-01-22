package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

var erc20ABI abi.ABI

var erc20BIN []byte

func parseUint64orHex(str string) (uint64, error) {
	base := 10
	if strings.HasPrefix(str, "0x") {
		str = str[2:]
		base = 16
	}
	return strconv.ParseUint(str, base, 64)
}

func toHexBigInt(n *big.Int) string {
	if n == nil {
		return ""
	}
	return fmt.Sprintf("0x%x", n) // or %X or upper case
}

func toHexUint64(i uint64) string {
	return fmt.Sprintf("0x%x", i)
}

func (t *transaction) waitForTxn(timeout ...time.Duration) error {
	if (t.hash == common.Hash{}) {
		panic("transaction not executed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if len(timeout) > 0 {
		go func() {
			time.Sleep(timeout[0])
			cancel()
		}()
	}

	for {
		var receipt map[string]interface{}
		if err := t.client.Call(&receipt, "eth_getTransactionReceipt", t.hash); err != nil {
			if err.Error() != "not found" {
				return err
			}
		}

		if receipt != nil {
			t.receipt = receipt
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type transaction struct {
	client  *rpc.Client
	hash    common.Hash
	receipt map[string]interface{}

	from     common.Address
	to       *common.Address
	amount   *big.Int
	nonce    uint64
	gasLimit uint64
	gasPrice uint64
	data     []byte
}

func (t *transaction) ToString() map[string]string {
	attr := map[string]string{}
	attr["from"] = t.from.Hex()
	if t.to != nil {
		attr["to"] = t.to.Hex()
	}
	attr["value"] = toHexBigInt(t.amount)
	attr["nonce"] = toHexUint64(t.nonce)
	attr["gas"] = toHexUint64(t.gasLimit)
	attr["gasPrice"] = toHexUint64(t.gasPrice)

	if len(t.data) != 0 {
		attr["data"] = "0x" + hex.EncodeToString(t.data)
	} else {
		attr["data"] = "0x"
	}
	return attr
}

var dummyAddresses = []common.Address{
	common.HexToAddress("0xEdc8057E1EDd75ab592Eaa863D50e26B2CE0682d"),
}

type env struct {
	client   *rpc.Client
	accounts []common.Address
}

func newEnv(client *rpc.Client) *env {
	var accounts []common.Address
	if err := client.Call(&accounts, "eth_accounts"); err != nil {
		panic(err)
	}
	return &env{
		client:   client,
		accounts: accounts,
	}
}

func (e *env) BalanceOfAddr(i int) *big.Int {
	w := &wallet{client: e.client, addr: e.Address(i)}
	return w.Balance()
}

func (e *env) Address(i int) common.Address {
	return dummyAddresses[i]
}

func (e *env) Wallet(i int) *wallet {
	return &wallet{
		client: e.client,
		addr:   e.accounts[i],
	}
}

type wallet struct {
	addr   common.Address
	client *rpc.Client

	sendTxnHandler func(txn *transaction) error

	// used only for sendTxn2
	nonce     uint64
	nonceOnce sync.Once
	nonceLock sync.Mutex
}

func (w *wallet) TransferWithNonceAndGas(to common.Address, amount *big.Int, nonce uint64, gasPrice uint64) (*transaction, error) {
	txn := &transaction{
		to:       &to,
		amount:   amount,
		nonce:    nonce,
		gasPrice: gasPrice,
	}
	if err := w.SendTxn(txn); err != nil {
		return nil, err
	}
	return txn, nil
}

func (w *wallet) Transfer(to common.Address, amount *big.Int) (*transaction, error) {
	txn := &transaction{
		to:     &to,
		amount: amount,
	}

	if err := w.SendTxn(txn); err != nil {
		return nil, err
	}
	return txn, nil
}

func (w *wallet) Balance() *big.Int {
	var out string
	if err := w.client.Call(&out, "eth_getBalance", w.addr, "latest"); err != nil {
		panic(err)
	}
	balance, ok := new(big.Int).SetString(out[2:], 16)
	if !ok {
		panic(fmt.Errorf("failed to convert to big.int"))
	}
	return balance
}

func (w *wallet) getNonce() uint64 {
	var count string

	if err := w.client.Call(&count, "eth_getTransactionCount", w.addr, "pending"); err != nil {
		panic(err)
	}
	res, err := parseUint64orHex(count)
	if err != nil {
		panic(err)
	}
	return res
}

func (w *wallet) gasPrice() uint64 {
	var out string
	if err := w.client.Call(&out, "eth_gasPrice"); err != nil {
		panic(err)
	}
	res, err := parseUint64orHex(out)
	if err != nil {
		panic(err)
	}
	return res
}

func (w *wallet) estimateGasDeploy(txn *transaction) uint64 {
	msg := map[string]interface{}{
		"data": "0x" + hex.EncodeToString(txn.data),
	}
	var out string
	if err := w.client.Call(&out, "eth_estimateGas", msg); err != nil {
		panic(err)
	}
	res, err := parseUint64orHex(out)
	if err != nil {
		panic(err)
	}
	return res
}

const transferSig = "0xa9059cbb"

func (w *wallet) estimateGas(txn *transaction) uint64 {

	var msg map[string]string
	if txn.to == nil {
		// contract deployment
		msg = map[string]string{
			"data": "0x" + hex.EncodeToString(txn.data),
		}
	} else {
		// transaction
		msg = map[string]string{
			"from": txn.from.Hex(),
			"to":   txn.to.Hex(),
			"data": "0x" + hex.EncodeToString(txn.data),
		}
	}

	if strings.HasPrefix(msg["data"], transferSig) {
		// its a transfer, hardcoded so that we can simulate transactions that fail on chain
		return 60000
	}
	if len(msg["data"]) > 10000 {
		// failed txn
		return 60000
	}

	var out string
	if err := w.client.Call(&out, "eth_estimateGas", msg); err != nil {
		panic(err)
	}
	res, err := parseUint64orHex(out)
	if err != nil {
		panic(err)
	}

	return res
}

func (w *wallet) SendTxn(txn *transaction) error {
	// set from
	txn.from = w.addr
	if txn.client == nil {
		txn.client = w.client
	}

	// gas price
	if txn.gasPrice == 0 {
		txn.gasPrice = w.gasPrice()
	}

	// estimate gas
	txn.gasLimit = w.estimateGas(txn)

	if w.sendTxnHandler != nil {
		return w.sendTxnHandler(txn)
	}

	// default to 1
	return w.sendTxn1(txn)
}

// This one does not get stuck if sendTransaction fails.
// However, this alone does not completely ensure that the transactions
// will work. We still need to do a tracking of the pending transactions
// and take care of issues like this TestTxnFailsOnEVMAndNonceNotIncreased.

func (w *wallet) sendTxn3(txn *transaction) error {
	w.nonceLock.Lock()
	defer w.nonceLock.Unlock()

	w.nonceOnce.Do(func() {
		w.nonce = w.getNonce()
	})

	txn.nonce = w.nonce

	// build the transaction
	res := txn.ToString()

	var hash common.Hash
	if err := w.client.Call(&hash, "eth_sendTransaction", res); err != nil {
		return err
	}
	txn.hash = hash

	// everything worked, increase the nonce
	w.nonce++
	return nil
}

// In this case we do not rely exclusively on the getNonce endpoint to get the nonce
// we also keep a simple nonce count ourselves.
// In this case, we need to lock to acquire the nonce, otherwise, parallel calls to
// sendTxn may get the same nonce (this happens 1/1000)
// Note that if eth_sendTransaction fails, the nonce will have been increased
// and the rest of the txns will get stuck (Check TestSendTxn2GetsStuck)

func (w *wallet) sendTxn2(txn *transaction) error {
	w.nonceOnce.Do(func() {
		w.nonce = w.getNonce()
	})

	w.nonceLock.Lock()
	txn.nonce = w.nonce
	w.nonce++
	w.nonceLock.Unlock()

	// build the transaction
	res := txn.ToString()

	var hash common.Hash
	if err := w.client.Call(&hash, "eth_sendTransaction", res); err != nil {
		return err
	}
	txn.hash = hash
	return nil
}

// this send transaction is like the one used in the Geth SDK.
// it is open for inconsistency with the nonce.
// We may get a repeated nonce if we send too many transactions

func (w *wallet) sendTxn1(txn *transaction) error {
	// get the nonce
	if txn.nonce == 0 {
		nonce := w.getNonce()
		txn.nonce = nonce
	}

	// build the transaction
	res := txn.ToString()

	var hash common.Hash
	if err := w.client.Call(&hash, "eth_sendTransaction", res); err != nil {
		return err
	}
	txn.hash = hash
	return nil
}

func erc20Token(wallet *wallet) *contract {
	return &contract{
		abi:    erc20ABI,
		bin:    erc20BIN,
		wallet: wallet,
		client: wallet.client,
	}
}

type contract struct {
	abi    abi.ABI
	bin    []byte
	addr   common.Address
	wallet *wallet
	client *rpc.Client
}

func (c *contract) Deploy() (*transaction, error) {
	if c.addr != (common.Address{}) {
		panic("Contract already deployed")
	}

	txn := &transaction{
		data: c.bin,
	}
	if err := c.wallet.SendTxn(txn); err != nil {
		panic(err)
	}

	if err := txn.waitForTxn(); err != nil {
		return nil, err
	}

	addr := txn.receipt["contractAddress"].(string)
	c.addr = common.HexToAddress(addr)

	return txn, nil
}

func (c *contract) BalanceOf(addr common.Address) *big.Int {
	var res *big.Int
	c.Call(&res, "balanceOf", addr)
	return res
}

func (c *contract) FailedTxn() (*transaction, error) {
	// this transaction will always failed on eth_sendTransaction

	data := make([]byte, 15*1024*1024)
	for i := range data {
		data[i] = 1
	}

	txn := &transaction{
		data: data,
		to:   &c.addr,
	}
	if err := c.wallet.SendTxn(txn); err != nil {
		return nil, err
	}
	return txn, nil
}

func (c *contract) Transfer(to common.Address, value *big.Int) (*transaction, error) {
	return c.SendTxn("transfer", to, value)
}

func (c *contract) Mint(to common.Address, value *big.Int) (*transaction, error) {
	return c.SendTxn("mint", to, value)
}

func (c *contract) MintWithNonce(to common.Address, value *big.Int, nonce uint64) (*transaction, error) {
	if c.addr == (common.Address{}) {
		panic("Contract not deployed")
	}
	input, err := c.abi.Pack("mint", to, value)
	if err != nil {
		panic(err)
	}

	txn := &transaction{
		data:  input,
		to:    &c.addr,
		nonce: nonce,
	}
	if err := c.wallet.SendTxn(txn); err != nil {
		return nil, err
	}
	return txn, nil
}

func (c *contract) Call(out interface{}, method string, args ...interface{}) {
	if c.addr == (common.Address{}) {
		panic("Contract not deployed")
	}
	input, err := c.abi.Pack(method, args...)
	if err != nil {
		panic(err)
	}

	msg := map[string]string{
		"from": c.wallet.addr.Hex(),
		"to":   c.addr.Hex(),
		"data": "0x" + hex.EncodeToString(input),
	}

	var res string
	if err := c.client.Call(&res, "eth_call", msg, "latest"); err != nil {
		panic(err)
	}

	resBytes, err := hex.DecodeString(strings.TrimPrefix(res, "0x"))
	if err != nil {
		panic(err)
	}

	if err := c.abi.Unpack(&out, method, resBytes); err != nil {
		panic(err)
	}
}

func (c *contract) SendTxn(method string, args ...interface{}) (*transaction, error) {
	if c.addr == (common.Address{}) {
		panic("Contract not deployed")
	}
	input, err := c.abi.Pack(method, args...)
	if err != nil {
		panic(err)
	}

	txn := &transaction{
		data: input,
		to:   &c.addr,
	}
	if err := c.wallet.SendTxn(txn); err != nil {
		return nil, err
	}
	return txn, nil
}

func init() {
	res, err := ioutil.ReadFile("./ERC20.json")
	if err != nil {
		panic(err)
	}
	erc20ABI, err = abi.JSON(bytes.NewReader(res))
	if err != nil {
		panic(err)
	}

	res, err = ioutil.ReadFile("./ERC20.bin")
	if err != nil {
		panic(err)
	}
	erc20BIN, err = hex.DecodeString(strings.TrimPrefix(string(res), "0x"))
	if err != nil {
		panic(err)
	}
}

func main() {
	numStr := os.Args[1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		panic(err)
	}
	testInParalel(num)
}

func testInParalel(handler int) {
	rpcClient, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		panic(err)
	}

	env := newEnv(rpcClient)

	user1 := env.Wallet(0)

	switch handler {
	case 1:
		user1.sendTxnHandler = user1.sendTxn1
	case 2:
		user1.sendTxnHandler = user1.sendTxn2
	case 3:
		user1.sendTxnHandler = user1.sendTxn3
	default:
		panic("Pick either 1, 2 or 3")
	}

	erc20 := erc20Token(user1)
	if _, err := erc20.Deploy(); err != nil {
		panic(err)
	}

	txn, err := erc20.Mint(user1.addr, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	if err := txn.waitForTxn(); err != nil {
		panic(err)
	}

	n := 50
	m := 10

	total := uint64(n*m) - 50 // 50 will fail for sure
	count := uint64(0)

	for i := 0; i < n; i++ {
		var wg sync.WaitGroup
		for j := 0; j < m; j++ {
			wg.Add(1)

			go func(i, j int) {
				defer wg.Done()

				var txn *transaction
				var err error

				if (handler == 2 || handler == 3) && j == 5 {
					// simulate failing transactions
					erc20.FailedTxn()
					return
				}

				txn, err = erc20.Transfer(env.Address(0), big.NewInt(10))
				if err != nil {
					fmt.Println(err)
				}
				if err == nil {
					if err := txn.waitForTxn(2 * time.Second); err != nil {
						panic(err)
					}

					block := txn.receipt["blockNumber"].(string)
					fmt.Printf("Executed %d.%d on block %s\n", i, j, block)
					atomic.AddUint64(&count, 1)
				}

			}(i, j)
		}
		wg.Wait()
	}

	fmt.Printf("SUCCEDD: %d, FAILED: %d\n", count, total-count)
}
