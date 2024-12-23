package gateway

import (
	"context"
	"errors"
	"time"

	"go.viam.com/rdk/components/board"
	"go.viam.com/utils"
)

const msToWait = 100

// it is necessary to sleep betweeen setting the gpio pins - the gateway will not initialize correctly without it.
func waitGPIO(ctx context.Context) error {
	if !utils.SelectContextOrWait(ctx, msToWait*time.Millisecond) {
		return errors.New("context cancelled")
	}
	return nil
}

func resetGateway(ctx context.Context, rst, pwr board.GPIOPin) error {
	if pwr != nil {
		err := pwr.Set(ctx, true, nil)
		if err != nil {
			return err
		}

		if err := waitGPIO(ctx); err != nil {
			return err
		}
	}

	err := rst.Set(ctx, true, nil)
	if err != nil {
		return err
	}

	if err := waitGPIO(ctx); err != nil {
		return err
	}

	err = rst.Set(ctx, false, nil)
	if err != nil {
		return err
	}

	if err := waitGPIO(ctx); err != nil {
		return err
	}
	return nil
}
