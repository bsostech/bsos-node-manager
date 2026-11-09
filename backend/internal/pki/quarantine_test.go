package pki

import "testing"

// Quarantine state is derived from revocation records, not recorded only when the node dials in: revocation
// is an action the platform itself takes, so the state holds from the moment it happens.
//
// syncQuarantine needs a DB to run, so this pins down its only pure logic: write suppression. Without
// suppression, the deny watcher (one round every 2 seconds) would issue an UPDATE per node each round.
func TestQuarantineStateSuppressesRepeatWrites(t *testing.T) {
	s := &Service{}

	if !s.quarantineChanged("node-a", true) {
		t.Fatal("第一次看到就該寫")
	}
	if s.quarantineChanged("node-a", true) {
		t.Fatal("狀態沒變就不該再寫")
	}
	if !s.quarantineChanged("node-a", false) {
		t.Fatal("撤銷被解除(換了新憑證)時要寫回去")
	}
	if !s.quarantineChanged("node-b", false) {
		t.Fatal("另一個節點第一次看到也該寫")
	}
}

// A failed DB write must clear the marker; otherwise that node would not retry until its state changes again,
// and "revoked" is a state that never changes again.
func TestForgetQuarantineStateAllowsRetry(t *testing.T) {
	s := &Service{}
	s.quarantineChanged("node-a", true)

	s.forgetQuarantineState("node-a")

	if !s.quarantineChanged("node-a", true) {
		t.Fatal("撤掉記號後應該重試")
	}
}
