package pricing

import (
	"fmt"
	"math"
)

// MicroUSD is an exact integer number of one-millionth US dollars.
type MicroUSD int64

const RedisSafeLimit MicroUSD = 1<<53 - 1

func (money MicroUSD) Valid() bool { return money >= 0 && money <= RedisSafeLimit }

func (money MicroUSD) Add(other MicroUSD) (MicroUSD, error) {
	if money < 0 || other < 0 || money > RedisSafeLimit || other > RedisSafeLimit || other > RedisSafeLimit-money {
		return 0, fmt.Errorf("microUSD addition overflows safe range")
	}
	return money + other, nil
}

func (money MicroUSD) Sub(other MicroUSD) (MicroUSD, error) {
	if money < 0 || other < 0 || money > RedisSafeLimit || other > RedisSafeLimit || other > money {
		return 0, fmt.Errorf("microUSD subtraction would be negative")
	}
	return money - other, nil
}

func (money MicroUSD) Int64() int64 { return int64(money) }

func checkedInt64(value int64) error {
	if value < 0 || value > math.MaxInt64 {
		return fmt.Errorf("value is outside non-negative int64 range")
	}
	return nil
}
