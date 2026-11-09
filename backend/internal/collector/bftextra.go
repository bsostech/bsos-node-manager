package collector

import (
	"encoding/hex"
	"errors"
	"strings"
)

// IBFT2 / QBFT block extraData is an RLP list: [vanity, validators[], vote, round, seals[]] (QBFT may carry extra trailing fields, which are not read).
// Three things are extracted: validators (the validator set when this block was produced; this block's vote is reflected only from the next block), round (how many proposer changes this height took before it was produced) and vote (the vote this block carries).
// With no vote it is an empty string (0x80); otherwise [address, type], where type 0xff = add and anything else = remove (a local IBFT2 network was observed using 0x00).
// Only the bytes and list RLP kinds are needed, so geth is not pulled in for this.

// BlockVote: the vote carried in a block. Target is 0x + lowercase address.
type BlockVote struct {
	Target string
	Add    bool
}

type rlpItem struct {
	str    []byte
	list   []rlpItem
	isList bool
}

var errRLP = errors.New("rlp: malformed")

// rlpDecode: decodes one item and returns the remaining bytes.
func rlpDecode(b []byte) (rlpItem, []byte, error) {
	if len(b) == 0 {
		return rlpItem{}, nil, errRLP
	}
	p := b[0]
	var off, n int
	isList := false
	switch {
	case p < 0x80:
		return rlpItem{str: b[:1]}, b[1:], nil
	case p <= 0xb7:
		off, n = 1, int(p-0x80)
	case p <= 0xbf:
		off, n = rlpLen(b, int(p-0xb7))
	case p <= 0xf7:
		off, n, isList = 1, int(p-0xc0), true
	default:
		off, n = rlpLen(b, int(p-0xf7))
		isList = true
	}
	if off <= 0 || n < 0 || off+n > len(b) {
		return rlpItem{}, nil, errRLP
	}
	body, rest := b[off:off+n], b[off+n:]
	if !isList {
		return rlpItem{str: body}, rest, nil
	}
	it := rlpItem{isList: true}
	for len(body) > 0 {
		sub, r, err := rlpDecode(body)
		if err != nil {
			return rlpItem{}, nil, err
		}
		it.list = append(it.list, sub)
		body = r
	}
	return it, rest, nil
}

// rlpLen: long form where the length is encoded in k bytes; returns (data start, data length), or (0, -1) if invalid.
func rlpLen(b []byte, k int) (int, int) {
	if k < 1 || k > 4 || 1+k > len(b) {
		return 0, -1
	}
	n := 0
	for _, x := range b[1 : 1+k] {
		n = n<<8 | int(x)
	}
	return 1 + k, n
}

// bftExtra: fields decoded from extraData. Validators are 0x + lowercase addresses, in extraData order.
type bftExtra struct {
	Validators []string
	Round      int
	Vote       *BlockVote
}

// parseBftExtra: extracts the validator set, round and vote from extraData (0x hex).
func parseBftExtra(extra string) (bftExtra, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(extra, "0x"))
	if err != nil {
		return bftExtra{}, err
	}
	it, _, err := rlpDecode(raw)
	if err != nil || !it.isList || len(it.list) < 4 {
		return bftExtra{}, errRLP
	}
	var out bftExtra
	for _, v := range it.list[1].list {
		if len(v.str) == 20 {
			out.Validators = append(out.Validators, "0x"+hex.EncodeToString(v.str))
		}
	}
	if v := it.list[2]; v.isList && len(v.list) == 2 && len(v.list[0].str) == 20 {
		t := v.list[1].str
		out.Vote = &BlockVote{Target: "0x" + hex.EncodeToString(v.list[0].str), Add: len(t) == 1 && t[0] == 0xff}
	}
	for _, x := range it.list[3].str {
		out.Round = out.Round<<8 | int(x)
	}
	return out, nil
}
