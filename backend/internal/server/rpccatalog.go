package server

import "besu-manager/backend/internal/api"

// rpcMethodCatalog: Besu RPC method catalog, a curated list of "namespaces + common methods
// operators actually grant". Two uses: (1) source for the frontend picker (two granularities: whole
// namespace / single method); (2) backend validatePermissions allowlist (rejects mistyped
// permission strings). Deliberately curated, not exhaustive; maintained alongside Besu versions.
// Method names have no namespace prefix (Besu JSON-RPC convention <namespace>_<method>, permission
// syntax <namespace>:<method>).
var rpcMethodCatalog = []api.RpcNamespaceCatalog{
	{Namespace: "eth", Description: "Account balances, transactions, blocks, contract calls (most commonly granted)", Methods: []api.RpcCatalogMethod{
		{Method: "blockNumber", Description: "Current block number"},
		{Method: "getBalance", Description: "Get account balance"},
		{Method: "call", Description: "Read-only contract call (not sent to the chain)"},
		{Method: "estimateGas", Description: "Estimate gas"},
		{Method: "sendRawTransaction", Description: "Send a signed transaction"},
		{Method: "getTransactionReceipt", Description: "Get transaction receipt"},
		{Method: "getTransactionByHash", Description: "Get transaction by hash"},
		{Method: "getTransactionCount", Description: "Get account nonce"},
		{Method: "getBlockByNumber", Description: "Get block by number"},
		{Method: "getBlockByHash", Description: "Get block by hash"},
		{Method: "getLogs", Description: "Get event logs"},
		{Method: "gasPrice", Description: "Current gas price"},
		{Method: "chainId", Description: "Chain ID"},
		{Method: "syncing", Description: "Sync status"},
	}},
	{Namespace: "net", Description: "Network and peer information", Methods: []api.RpcCatalogMethod{
		{Method: "version", Description: "Network ID"},
		{Method: "peerCount", Description: "Peer count"},
		{Method: "enode", Description: "This node's enode"},
		{Method: "listening", Description: "Whether P2P is listening"},
	}},
	{Namespace: "web3", Description: "Node client information and hashing", Methods: []api.RpcCatalogMethod{
		{Method: "clientVersion", Description: "Besu client version"},
		{Method: "sha3", Description: "Keccak-256 hash"},
	}},
	{Namespace: "txpool", Description: "Transaction pool status (Besu only)", Methods: []api.RpcCatalogMethod{
		{Method: "besuStatistics", Description: "Transaction pool statistics"},
		{Method: "besuTransactions", Description: "Transactions in the pool"},
		{Method: "besuPendingTransactions", Description: "Pending transactions"},
	}},
	{Namespace: "admin", Description: "Node administration (add/remove peers, node info; high privilege)", Methods: []api.RpcCatalogMethod{
		{Method: "peers", Description: "Connected peers"},
		{Method: "nodeInfo", Description: "This node's info (enode etc.)"},
		{Method: "addPeer", Description: "Add a peer manually"},
		{Method: "removePeer", Description: "Remove a peer manually"},
	}},
	{Namespace: "debug", Description: "Debugging and tracing (expensive; grant with care)", Methods: []api.RpcCatalogMethod{
		{Method: "traceTransaction", Description: "Trace a transaction's execution"},
		{Method: "traceBlock", Description: "Trace a whole block"},
		{Method: "storageRangeAt", Description: "Read a contract storage range"},
	}},
	{Namespace: "trace", Description: "Transaction tracing (OpenEthereum style; expensive)", Methods: []api.RpcCatalogMethod{
		{Method: "transaction", Description: "Trace a transaction"},
		{Method: "block", Description: "Trace a whole block"},
		{Method: "replayBlockTransactions", Description: "Replay block transactions for traces"},
	}},
	{Namespace: "miner", Description: "Block production / coinbase control (private chain operations; high privilege)", Methods: []api.RpcCatalogMethod{
		{Method: "start", Description: "Start producing blocks"},
		{Method: "stop", Description: "Stop producing blocks"},
		{Method: "setCoinbase", Description: "Set coinbase address"},
	}},
	{Namespace: "ibft", Description: "IBFT2 validator governance", Methods: []api.RpcCatalogMethod{
		{Method: "getValidatorsByBlockNumber", Description: "Get validators at a block"},
		{Method: "proposeValidatorVote", Description: "Vote to add/remove a validator"},
		{Method: "discardValidatorVote", Description: "Discard own vote"},
		{Method: "getPendingVotes", Description: "Get pending votes"},
	}},
	{Namespace: "qbft", Description: "QBFT validator governance", Methods: []api.RpcCatalogMethod{
		{Method: "getValidatorsByBlockNumber", Description: "Get validators at a block"},
		{Method: "proposeValidatorVote", Description: "Vote to add/remove a validator"},
		{Method: "discardValidatorVote", Description: "Discard own vote"},
		{Method: "getPendingVotes", Description: "Get pending votes"},
	}},
}

// catalogNamespaces: namespace -> set of valid methods (used by validatePermissions; built on first call).
var catalogNamespaces = func() map[string]map[string]bool {
	m := make(map[string]map[string]bool, len(rpcMethodCatalog))
	for _, ns := range rpcMethodCatalog {
		methods := make(map[string]bool, len(ns.Methods))
		for _, mth := range ns.Methods {
			methods[mth.Method] = true
		}
		m[ns.Namespace] = methods
	}
	return m
}()
