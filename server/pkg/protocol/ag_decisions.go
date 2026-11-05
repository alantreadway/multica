package protocol

// EventAGDecisionChanged tells a workspace's clients that the Decision Desk's
// cards changed. It carries no data: clients refetch.
const EventAGDecisionChanged = "ag:decision_changed"
