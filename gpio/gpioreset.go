// Package gpio sets the gateway's gpio pins
package gpio

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

func waitGPIO() {
	time.Sleep(100 * time.Millisecond)
}

func pinctrlSet(pin, state string, bookworm bool) error {
	if bookworm {
		cmd := exec.Command("pinctrl", "set", pin, state)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("error setting GPIO %s to %s: %v", pin, state, err)
		}
	} else {
		fmt.Println("here using thisraspi-gpioe")
		cmd := exec.Command("raspi-gpio", "set", pin, state)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("error setting GPIO %s to %s: %v", pin, state, err)
		}
	}
	return nil
}

// InitGateway initializes the gateway hardware.
func InitGateway(resetPin, powerPin *int, bookworm bool) error {
	rst := strconv.Itoa(*resetPin)
	var pwr string
	if powerPin != nil {
		pwr = strconv.Itoa(*powerPin)
	}
	err := initGPIO(rst, pwr, bookworm)
	if err != nil {
		return err
	}
	err = resetGPIO(rst, bookworm)
	if err != nil {
		return err
	}
	return nil
}

func initGPIO(resetPin, powerPin string, bookworm bool) error {
	// Set GPIOs as output
	err := pinctrlSet(resetPin, "op", bookworm)
	if err != nil {
		return err
	}
	waitGPIO()
	if powerPin != "" {
		err := pinctrlSet(powerPin, "op", bookworm)
		if err != nil {
			return err
		}
		waitGPIO()
	}
	return nil
}

func resetGPIO(resetPin string, bookworm bool) error {
	err := pinctrlSet(resetPin, "dh", bookworm)
	if err != nil {
		return err
	}
	waitGPIO()
	err = pinctrlSet(resetPin, "dl", bookworm)
	if err != nil {
		return err
	}
	waitGPIO()
	return nil
}
