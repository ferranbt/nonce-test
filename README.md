Dummy change

This program describes techniques that can be used to increment the nonce of an ethereum dapp. It simulates the parallel execution of transactions. It runs 50 iterations that send 10 transactions in parallel: 9 of them valid and 1 invalid that fails on the eth_sendTransaction endpoint.

Run the test docker client. It is important to run Geth because Ganache has some inconsistencies in how the txns are processed.

```
$ docker run --net=host ethereum/client-go --dev --rpc --rpcport 8545
```

The program includes three ways or methods to execute the transactions:

## SendTxn1

Method used in the Geth SDK client. Every time it sends a transaction, it will query the eth_getTransactionCount endpoint to get the last nonce. The issue is that the queries to this endpoint are done in parallel. Thus, we will get repeated and invalid nonces.

Run it:

```
$ go run main.go 1
```

There will be a lot of 'nonce too low' or 'known transaction' issues by transactions that are not executed because they were assigned already used nonces.

## SendTxn2

This method keeps an internal count of the nonce (it only queries once the endpoint to get the starting nonce). Thus, we do not rely on the racy eth_getTransactionCount to get the nonce anymore. However, if the transaction fails on the eth_sendTransaction endpoint, the nonce gets increased anyway and the sequence is lost, transactions will get stuck on the txnpool. You will see timeout erros in this case, meaning that some transactions are never being executed.

Run:

```
$ go run main.go 2
```

Beware: This method leaves the Geth client in an inconsistent state (with pending txns on the pool). It is recomended to restart the clinet after this test.

## SendTxn3

This method locks the whole send transaction routine, so that if eth_sendTransaction fails, the nonce is not increased. We still keep an internal count on the nonces. With this method the transactions do not fail.

```
$ go run main.go 3
```

Does this solve all the problems? No

The assumption made here is that if the transaction does not fail on the eth_sendRawTransaction endpoint his nonce will be increased. That is not completely true, there are a couple of side cases in which that may not happen (see test TestTxnFailsOnEVMAndNonceNotIncreased in main_test.go). We would need some sort of extra routine to keep track of all the pending transactions and making sure that they are really executed.

