package main

import (
	"alpha"
	"common/utils"
	"fmt"
	"math"
	"orderbook"
	"orderbook/md/pb"
	"os"
	"time"

	json "github.com/json-iterator/go"
)

const Sec2Nanos = 1e9

type Policy struct {
	ShieldHalflifeSec float64
	BidShield         float64
	BidShieldTime     uint64
	BidFillNum        int
	AskShield         float64
	AskShieldTime     uint64
	AskFillNum        int
}

type PolicyGrid struct {
	ShieldHalflifeSecGrid []float64 `json:"shieldHalflifeSecGrid"`
}

type Tick struct {
	Type      string  `json:"e"`
	Id        int64   `json:"u"`
	PushTime  int64   `json:"E"`
	Time      int64   `json:"T"`
	Symbol    string  `json:"s"`
	BidPrice  float64 `json:"b,string"`
	BidVolume float64 `json:"B,string"`
	AskPrice  float64 `json:"a,string"`
	AskVolume float64 `json:"A,string"`
}

var symbol = os.Args[1]
var redis = utils.NewRedisClient()
var policies []*Policy
var policyGrid *PolicyGrid
var fewap = alpha.NewFewap(symbol)
var bidPrice float64
var askPrice float64
var sec1LowBidPrice float64
var sec1HighAskPrice float64

func init() {
	policyGridMap := map[string]*PolicyGrid{}
	data, err := os.ReadFile("./policy_grid.json")
	if err != nil {
		panic(err)
	}

	err = json.Unmarshal(data, &policyGridMap)
	if err != nil {
		panic(err)
	}
	policyGrid = policyGridMap[symbol]

	for _, shieldHalflifeSec := range policyGrid.ShieldHalflifeSecGrid {
		policies = append(policies,
			&Policy{
				ShieldHalflifeSec: shieldHalflifeSec,
				BidFillNum:        1,
				AskFillNum:        1,
			},
		)
	}
}

func runBook() {
	sec1bookHistory := []*pb.L2BookProto{}
	bookChan := orderbook.GetBookChan(symbol)
	for book := range bookChan {
		fewap.Update(book)
		sec1bookHistory = append(sec1bookHistory, book)
		for i, bookRecord := range sec1bookHistory {
			if book.LocalTs-bookRecord.LocalTs < 1e9 { // >1s
				sec1bookHistory = sec1bookHistory[i:]
				break
			}
		}

		for _, book := range sec1bookHistory {
			if book.LevelsBids[0].Price < sec1LowBidPrice {
				sec1LowBidPrice = book.LevelsBids[0].Price
			}
			if book.LevelsAsks[0].Price > sec1HighAskPrice {
				sec1HighAskPrice = book.LevelsAsks[0].Price
			}
		}

		bidPrice = book.LevelsBids[0].Price
		askPrice = book.LevelsAsks[0].Price
		newBidShield := math.Log(sec1bookHistory[0].LevelsBids[0].Price / bidPrice)
		newAskShield := math.Log(askPrice / sec1bookHistory[0].LevelsAsks[0].Price)

		for _, policy := range policies {
			policy.BidShield *= 1 / math.Pow(2, float64(book.LocalTs-policy.BidShieldTime)/(policy.ShieldHalflifeSec*Sec2Nanos))
			policy.AskShield *= 1 / math.Pow(2, float64(book.LocalTs-policy.AskShieldTime)/(policy.ShieldHalflifeSec*Sec2Nanos))
			policy.BidShieldTime = book.LocalTs
			policy.AskShieldTime = book.LocalTs

			if newBidShield > policy.BidShield {
				policy.BidShield = newBidShield
				policy.BidShieldTime = book.LocalTs
			}
			if newAskShield > policy.AskShield {
				policy.AskShield = newAskShield
				policy.AskShieldTime = book.LocalTs
			}
		}
	}
}

const quoteInterval = 200 * time.Millisecond
const cancelDelay = 1000 * time.Millisecond

func main() {
	go monitor()
	go runBook()

	for {
		time.Sleep(quoteInterval)
		for _, policy := range policies {
			go policy.setOrder()
		}
	}
}

func (p *Policy) setOrder() {
	if fewap.Signal > 0 { // buy
		price := bidPrice * (1 - p.BidShield - 5e-4)
		time.Sleep(cancelDelay)
		if price > sec1LowBidPrice {
			p.BidFillNum += 1
		}
	} else { // sell
		price := askPrice * (1 + p.AskShield + 5e-4)
		time.Sleep(cancelDelay)
		if price < sec1HighAskPrice {
			p.AskFillNum += 1
		}
	}
}

func monitor() {
	for {
		time.Sleep(1 * time.Second)
		msg := ""
		for i, policy := range policies {
			msg += fmt.Sprintf("[\"%.0f\"{%d|%d}%.2f] ", policyGrid.ShieldHalflifeSecGrid[i], policy.BidFillNum, policy.AskFillNum, float64(policy.BidFillNum/policy.AskFillNum))
		}
		fmt.Println(msg)
	}
}
