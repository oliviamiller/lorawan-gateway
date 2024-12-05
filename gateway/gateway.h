#pragma once

#include <stdint.h>
#include "../sx1302/libloragw/inc/loragw_hal.h"

struct lgw_pkt_rx_s* createRxPacketArray();
int receive(struct lgw_pkt_rx_s* packet);
int send(struct lgw_pkt_tx_s* packet);
int stopGateway();
int setUpGateway(int com_path);
void disableBuffering();
void redirectToPipe(int fd);
