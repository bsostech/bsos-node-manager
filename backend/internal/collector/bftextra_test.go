package collector

import (
	"strings"
	"testing"
)

// Samples from a local four-node IBFT2 network (Besu 26.7): block 9445 carries an add vote, 9449 a remove vote, 9450 no vote.
const (
	extraAddVote  = "0xf9015fa0000000000000000000000000000000000000000000626573752032362e372e31f8549410fd2c060eed3590a5f7d538435ccf607b51fb9c94120cee331c15b3f6e129a80c1540167bd2f9a73394adf09c7b3577a197103f666bae81211dc3592f0994d05def05060bd2f84e4d7f756fffae9c275006f0d794111111111111111111111111111111111111111181ff8400000000f8c9b8417cac3f7425bcc7cb2823cfa23909f5194989b01bb33a7ce6a0e23998fd6993d27b632ad37135d5ca2138c1878ba01c17adf67c018332f2c235e4f68b61cecf5800b841afebae5dc04c7c21fa4f5d4abd42733ad0323362e8e12fd493f6d86aa1c3e58014a66d0617364b41f23cf1a6b0121a45b125806ca73d8cce6d2a5726ffad6b3900b84119b04b3cd3e9e9c68b32c0b6713ceee8c1b9548fc3a4d5beb06ce7aa3a32f50d51eb532d821795bb31283c89d4654625052cb00655fbb305282a2d586ad560e700"
	extraDropVote = "0xf9015ea0000000000000000000000000000000000000000000626573752032362e372e31f8549410fd2c060eed3590a5f7d538435ccf607b51fb9c94120cee331c15b3f6e129a80c1540167bd2f9a73394adf09c7b3577a197103f666bae81211dc3592f0994d05def05060bd2f84e4d7f756fffae9c275006f0d694120cee331c15b3f6e129a80c1540167bd2f9a733008400000000f8c9b8410704ee821f16c60eb0a1259b11e3e05c5d466322411071de17aaa8b51af24dd0630c252406c112a434849d2e2fd213100b2f3b9f1493fe2338d43f3b99c2441400b8419e5855d4e077dbff1a600d6aa5243584547ed2668126b7c3b8cc212c5080c99e0d8f7987f42953dbb12b235f4bfa24ab79e24e588a9d1e2c695066c089c89cf601b8419c5d844fa4eefcd94eac924bee8b55e6076d5e88df2efbe4e1d6a9471b2c1e0d7d23e5c8dc9003197d774a47ef1ad5f5de66f5df3bce4e51d609dcfd85c50c3e00"
	extraNoVote   = "0xf90148a0000000000000000000000000000000000000000000626573752032362e372e31f8549410fd2c060eed3590a5f7d538435ccf607b51fb9c94120cee331c15b3f6e129a80c1540167bd2f9a73394adf09c7b3577a197103f666bae81211dc3592f0994d05def05060bd2f84e4d7f756fffae9c275006f0808400000000f8c9b841a820bfb2a602706461e5bab3350e18aceb1c21aaf065d0444c9be18a067fd19671ce03000c2ea8a9a3a8b5495143ee0dfba16307808a7c08e088b4db281614f300b8413591efc66e7cb4afe288bd36e49c92782477c9457aba3b569ac3c498c070e06040b3601491821d33f1b7c8c19aec96bdb5bf3f8ab46ea4557696c7d11b12276601b8419b14b809fbed9eda665455a886cfb4075c6c211f7df2814aa993ef008d8da98060fbb8e34e968f88a4ab8cce2d18c43154bb25fb2330a057753110af8fa1424801"
)

func TestParseBftExtra(t *testing.T) {
	x, err := parseBftExtra(extraAddVote)
	if err != nil || x.Round != 0 || x.Vote == nil || !x.Vote.Add || x.Vote.Target != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("add vote: %+v err=%v", x, err)
	}
	if len(x.Validators) != 4 || x.Validators[0] != "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c" {
		t.Fatalf("validators: %v", x.Validators)
	}
	x, err = parseBftExtra(extraDropVote)
	if err != nil || x.Round != 0 || x.Vote == nil || x.Vote.Add || x.Vote.Target != "0x120cee331c15b3f6e129a80c1540167bd2f9a733" {
		t.Fatalf("drop vote: %+v err=%v", x, err)
	}
	x, err = parseBftExtra(extraNoVote)
	if err != nil || x.Round != 0 || x.Vote != nil {
		t.Fatalf("no vote: %+v err=%v", x, err)
	}
	// round is a fixed 4 bytes (IBFT2); changing it to 2 = the proposer changed twice at this height
	bumped := strings.Replace(extraNoVote, "808400000000", "808400000002", 1)
	if x, err := parseBftExtra(bumped); err != nil || x.Round != 2 {
		t.Fatalf("round: %d %v", x.Round, err)
	}
	if _, err := parseBftExtra("0x1234"); err == nil {
		t.Fatal("garbage should fail")
	}
	// Truncated input must return an error, not panic: 3 bytes declared but only 1 given, and a length field that is itself cut off
	for _, s := range []string{"0x8301", "0xb9"} {
		if _, err := parseBftExtra(s); err != errRLP {
			t.Fatalf("%s: err=%v", s, err)
		}
	}
}

// A truncated length field must be reported invalid; the slice is given spare capacity so b[1:1+k] does not panic on its own from going just out of bounds.
func TestRlpLenTruncated(t *testing.T) {
	b := append(make([]byte, 0, 8), 0xb9)
	if off, n := rlpLen(b, 2); off != 0 || n != -1 {
		t.Fatalf("rlpLen = (%d, %d), want (0, -1)", off, n)
	}
}

// The long-form length field is at most 4 bytes: both a string and a list with exactly 4 bytes must decode.
func TestRlpDecodeFourByteLength(t *testing.T) {
	if it, rest, err := rlpDecode([]byte{0xbb, 0, 0, 0, 2, 0xaa, 0xbb, 0x01}); err != nil || it.isList || string(it.str) != "\xaa\xbb" || len(rest) != 1 {
		t.Fatalf("字串:%+v rest=%v err=%v", it, rest, err)
	}
	if it, _, err := rlpDecode([]byte{0xfb, 0, 0, 0, 1, 0x05}); err != nil || !it.isList || len(it.list) != 1 || it.list[0].str[0] != 0x05 {
		t.Fatalf("list:%+v err=%v", it, err)
	}
}
