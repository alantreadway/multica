package protocol

// AG patch: runtime pool.

// AGPoolPace is what a heartbeat ack tells a pooled Claude runtime about its
// pool, so the daemon's Claude pacing can count the runs its peers have on the
// same account. Only peers that are online with a fresh heartbeat count. The
// server sends it only for a pooled runtime whose heartbeat carried an ag_quota
// report; a daemon that gets none paces on its own runs alone.
type AGPoolPace struct {
	// PeerActive is the dispatched and running tasks on the peers.
	PeerActive int `json:"peer_active"`
	// PeerSlots and Slots are the claim slots of the peers and of this runtime:
	// the lower of the daemon's reported max and the runtime cap, 0 while
	// draining. The daemon takes Slots / (Slots + PeerSlots) of the pool's runs.
	PeerSlots int `json:"peer_slots"`
	Slots     int `json:"slots"`
	// Lead is true for the one runtime of the pool with the most slots (the
	// lowest ID on a tie). It takes what the others' rounded-down shares leave.
	Lead bool `json:"lead"`
}
