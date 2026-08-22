package main

import (
	"fmt"
	"strings"
	"github.com/lib/pq"
)

func main() {
	outcomeFilter := "pre_interview,interview,decision_pending,reserved,different_account,contact_for_slot"
	outcomes := strings.Split(outcomeFilter, ",")
	fmt.Printf("%#v\n", pq.Array(outcomes))
}
