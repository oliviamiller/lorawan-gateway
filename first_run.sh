#!/bin/bash

# Function to check if the board is a Raspberry Pi
is_raspberry_pi() {
    local cpuinfo_content
    cpuinfo_content=$(cat /proc/cpuinfo)

    if [[ "$cpuinfo_content" == *"Raspberry Pi"* ]]; then
        return 0
    else
        echo "Module must be run on a raspberry pi"
        exit 1
    fi
}

# function to enable communication protocols such as SPI and I2C.
enable_protocol() {
    local protocol=$1
    local get_cmd="get_${protocol}"
    local do_cmd="do_${protocol}"

    # Check the current status of the protocol
    status=$(sudo raspi-config nonint "$get_cmd")
    if [[ $? -ne 0 ]]; then
        echo "Failed to get ${protocol} status. Ensure that raspi-config is installed."
        exit 1
    fi

    # Status of 1 means disabled, 0 means enabled
    if [[ "$status" == "1" ]]; then
        echo "Enabling ${protocol} on Raspberry Pi..."
        sudo raspi-config nonint "$do_cmd" 0
        if [[ $? -ne 0 ]]; then
            echo "Failed to enable ${protocol}. Please enable it manually using raspi-config."
            exit 1
        fi
        echo "${protocol} has been successfully enabled."
    else
        echo "${protocol} is already enabled."
    fi
}

#check that the module is being run on a pi.
is_raspberry_pi

# Enable I2C and SPI
enable_protocol "i2c"
enable_protocol "spi"
