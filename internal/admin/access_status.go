package admin

import "github.com/egerkuzma/emtds/internal/store"

// foldedStatus is the status of every row foldConfirmed returns. The interface
// counts it as a confirmed conversion, like a lead or a sale.
const foldedStatus = "sale"

// confirmedStatuses are the conversion statuses that count as confirmed: the rule
// of the dashboard series and the report.
var confirmedStatuses = map[string]bool{"lead": true, "sale": true}

// foldConfirmed hides the conversion status of rows from a viewer who may not see it.
//
// rows are the status facts. Only confirmed statuses stay; each (offer, network,
// campaign) key becomes one row with status foldedStatus and the summed count and
// profit and deposit. Rejected and other statuses are dropped, and the order of
// the first appearance of each key is kept. The result is never nil. rows is not modified.
func foldConfirmed(rows []store.StatusFact) []store.StatusFact {
	type key struct{ offer, network, group string }
	index := map[key]int{}
	out := []store.StatusFact{}
	for _, r := range rows {
		if !confirmedStatuses[r.Status] {
			continue
		}
		k := key{r.OfferID, r.NetworkID, r.GroupID}
		i, ok := index[k]
		if !ok {
			index[k] = len(out)
			out = append(out, store.StatusFact{OfferID: r.OfferID, NetworkID: r.NetworkID, GroupID: r.GroupID, Status: foldedStatus})
			i = len(out) - 1
		}
		out[i].N += r.N
		out[i].Profit += r.Profit
		out[i].DepSum += r.DepSum
	}
	return out
}
