package main

import (
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ory/dockertest"
)

func assertBigInt(t *testing.T, a, b *big.Int) {
	if a.Cmp(b) != 0 {
		t.Fatal("")
	}
}

func TestMint(t *testing.T) {
	rpcClient, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		panic(err)
	}

	env := newEnv(rpcClient)
	user1 := env.Wallet(0)

	erc20 := erc20Token(user1)
	erc20.Deploy()

	// mint tokens
	amount := big.NewInt(1000000)
	assertBigInt(t, erc20.BalanceOf(user1.addr), big.NewInt(0))
	txn, _ := erc20.Mint(user1.addr, amount)
	if err := txn.waitForTxn(); err != nil {
		panic(err)
	}
	assertBigInt(t, erc20.BalanceOf(user1.addr), amount)

	// transfer the tokens to account 2
	txn, _ = erc20.Transfer(env.Address(0), amount)
	if err := txn.waitForTxn(); err != nil {
		panic(err)
	}
	assertBigInt(t, erc20.BalanceOf(user1.addr), big.NewInt(0))
	assertBigInt(t, erc20.BalanceOf(env.Address(0)), amount)

	preNonce := user1.getNonce()

	// We are going to force a failure now, this transaction fails on the evm because
	// it tries to send more tokens than the available ones. This check is performed on
	// the EVM, thus, when sended to the eth_sendTransaction endpoint, it will go through,
	// but, when mined, it will fail. Nevertheless, the nonce will be increased.
	txn, _ = erc20.Transfer(env.Address(0), amount)
	if err := txn.waitForTxn(); err != nil {
		panic(err)
	}
	assertBigInt(t, erc20.BalanceOf(user1.addr), big.NewInt(0))
	assertBigInt(t, erc20.BalanceOf(env.Address(0)), amount)

	// nonce increases
	postNonce := user1.getNonce()
	if preNonce != postNonce-1 {
		t.Fatal("bad nonce")
	}
}

func TestSendTxnsBackwards(t *testing.T) {
	// This test does not use getTransactionCount to get the nonce, instead
	// we need to supply the correct sequence number

	rpcClient, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		panic(err)
	}

	env := newEnv(rpcClient)
	user1 := env.Wallet(0)

	erc20 := erc20Token(user1)
	erc20.Deploy()

	ammount := big.NewInt(100000)

	// get the current nonce
	nonce := user1.getNonce()

	// mint with nonce + 2
	nonce2 := nonce + 2
	txn2, err := erc20.MintWithNonce(user1.addr, ammount, nonce2)
	if err != nil {
		t.Fatal(err)
	}

	// mint with nonce + 1
	nonce1 := nonce + 1
	txn1, err := erc20.MintWithNonce(user1.addr, ammount, nonce1)
	if err != nil {
		t.Fatal(err)
	}

	// transactions will not finalize because there is no sequence yet
	if err := txn2.waitForTxn(3 * time.Second); err == nil {
		t.Fatal("txn2 cannot finalize yet")
	}
	if err := txn1.waitForTxn(3 * time.Second); err == nil {
		t.Fatal("txn2 cannot finalize yet")
	}

	// mint with correct nonce, the previous txns stuck in the pool will finalize now
	txn, err := erc20.MintWithNonce(user1.addr, ammount, nonce)
	if err != nil {
		t.Fatal(err)
	}

	// all the txns finalize now
	if err := txn.waitForTxn(1 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := txn1.waitForTxn(1 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := txn2.waitForTxn(1 * time.Second); err != nil {
		t.Fatal(err)
	}

	// we minted three times the ammount
	assertBigInt(t, erc20.BalanceOf(user1.addr), ammount.Mul(ammount, big.NewInt(3)))
}

func TestSendTxn2GetsStuck(t *testing.T) {
	rpcClient, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		panic(err)
	}

	env := newEnv(rpcClient)
	user1 := env.Wallet(0)
	user1.sendTxnHandler = user1.sendTxn2 // use the second send transaction

	erc20 := erc20Token(user1)
	erc20.Deploy()

	// send a transaction that will fail on the eth_sendRawTransaction endpoint
	if _, err := erc20.FailedTxn(); err == nil {
		t.Fatal("it should fail")
	}

	// nonce has been increased, send another valid transaction
	txn, err := erc20.Mint(user1.addr, big.NewInt(10000))
	if err != nil {
		t.Fatal(err)
	}
	// it will never be mined
	if err := txn.waitForTxn(3 * time.Second); err == nil {
		t.Fatal("it should timeout")
	}
}

func TestSendTxn3WorksWithFailingTxns(t *testing.T) {
	rpcClient, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		panic(err)
	}

	env := newEnv(rpcClient)
	user1 := env.Wallet(0)
	user1.sendTxnHandler = user1.sendTxn3 // use the third send transaction

	erc20 := erc20Token(user1)
	erc20.Deploy()

	// send a transaction that will fail on the eth_sendRawTransaction endpoint
	// the nonce wont be increased
	if _, err := erc20.FailedTxn(); err == nil {
		t.Fatal("it should fail")
	}

	// nonce has NOT been increased, send another valid transaction
	txn, err := erc20.Mint(user1.addr, big.NewInt(10000))
	if err != nil {
		t.Fatal(err)
	}
	// it will be mined
	if err := txn.waitForTxn(3 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func testGethServer(t *testing.T) (*rpc.Client, func()) {
	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("Could not connect to docker: %s", err)
	}

	opts := &dockertest.RunOptions{
		Repository: "ethereum/client-go",
		Cmd: []string{
			"--dev", "--rpc", "--rpcport", "8545", "--rpcaddr", "0.0.0.0",
		},
	}
	resource, err := pool.RunWithOptions(opts)
	if err != nil {
		t.Fatalf("Could not start resource: %s", err)
	}

	close := func() {
		if err := pool.Purge(resource); err != nil {
			t.Fatalf("Could not purge resource: %s", err)
		}
	}

	addr := "http://0.0.0.0:" + resource.GetPort("8545/tcp")

	if err := pool.Retry(func() error {
		resp, err := http.Post(addr, "application/json", nil)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}); err != nil {
		close()
		t.Fatalf("Could not connect to docker: %s", err)
	}

	client, err := rpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	return client, close
}

func TestTxnFailsOnEVMAndNonceNotIncreased(t *testing.T) {
	// this tests proves the only case in which there will be a
	// valid eth_sendRawTransaction return value but the txn will not
	// increase the nonce

	// When a transaction is send to eth_send(Raw)Transaction, the client performs
	// some validation checks: it has enough eth funds to send the txn, the calldata
	// size is not too big, the nonce is correct... It this checks are valid, the txn
	// is sent to the txn pool.

	// Once in the transaction pool and if the nonce is correct (in sequence with the current
	// nonce of the account) the txn can be picked to be executed and mined.
	// Once the txn is being mined and included in the block, the client performs the previous
	// checks again (i.e. funds, nonce...). If any of this checks fails, the txn fails
	// and the nonce IS NOT increased.
	// If this checks are valid, we can assume that the nonce WILL BE increased.

	// Thus, we can assume that if a transaction does not fail on the eth_send(Raw)Transaction
	// it is likely that the nonce of the account WILL BE increased. This tests checks the only
	// sidecase in which that may not the case.

	// Test.
	// 1. Send Txn B with nonce+1 (stuck in the pool).
	// 2. Send Txn A with nonce that consumes all the ether of the account.

	// Both transactions were valid once they were sent, however, txn B will fail once mined
	// because there are no more funds anymore on the account, all of them have been consumed by txn A.

	// we use a new test server everytime

	client, close := testGethServer(t)
	defer close()

	env := newEnv(client)
	user1 := env.Wallet(0)

	// keep only 100000000000000000 tokens
	keep := big.NewInt(100000000000000000)

	currentBalance := user1.Balance()
	txn, err := user1.Transfer(env.Address(0), currentBalance.Sub(currentBalance, keep))
	if err != nil {
		panic(err)
	}
	if err := txn.waitForTxn(); err != nil {
		panic(err)
	}

	fmt.Println(user1.Balance())

	// make sure we have all the tokens
	assertBigInt(t, user1.Balance(), keep)

	currentBalance = user1.Balance()
	sendQuantity := currentBalance.Sub(currentBalance, big.NewInt(10000000000))

	/*
		// The second operation WILL FAIL because the account does not have more tokens

		// send more tokens
		txn, err = user1.Transfer(env.Address(0), sendQuantity)
		if err != nil {
			panic(err)
		}
		if err := txn.waitForTxn(); err != nil {
			panic(err)
		}

		// send more tokens again should fail because there are no more funds
		txn, err = user1.Transfer(env.Address(0), sendQuantity)
		if err != nil {
			panic(err)
		}
		if err := txn.waitForTxn(); err != nil {
			panic(err)
		}
	*/

	// Lets send again those opereation but with a non sequential nonce.

	nonce := user1.getNonce()

	txn1, err := user1.TransferWithNonceAndGas(env.Address(0), sendQuantity, nonce+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	// it DOES NOT get executed YET (TXPOOL)
	if err := txn1.waitForTxn(2 * time.Second); err == nil {
		t.Fatal(err)
	}

	// send a good sequential transaction
	txn0, err := user1.TransferWithNonceAndGas(env.Address(0), sendQuantity, nonce, 10)
	if err != nil {
		t.Fatal(err)
	}
	// it gets executed
	if err := txn0.waitForTxn(); err != nil {
		t.Fatal(err)
	}

	// NOW: txn1 will get executed since it was on the pool, but, since there
	// are no more funds, it WILL FAIL silently, no receipts, no nonce increase.
	if err := txn1.waitForTxn(2 * time.Second); err == nil {
		t.Fatal(err)
	}

	// send another txn1' with nonce+1 with lower gas than txn1, thus,
	// if txn1 would still be on the pool, txn1' would not be executed because
	// it will not replace txn1.
	// if txn1' is valid and executed, that means that txn1 was not on the pool anymore.

	currentBalance = user1.Balance()
	sendQuantity = currentBalance.Sub(currentBalance, big.NewInt(1000000))

	txn1, err = user1.TransferWithNonceAndGas(env.Address(0), sendQuantity, nonce+1, 5)
	if err != nil {
		t.Fatal(err)
	}
	// it DOES get executed yet
	if err := txn1.waitForTxn(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}
