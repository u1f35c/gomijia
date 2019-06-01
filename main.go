//
// gomijia, a program to passively listen for Xiaomi Mijia temperature/humidity
// monitor advertisements over Bluetooth LE and report them via MQTT
//
// Copyright 2019 Jonathan McDowell <noodles@earth.li>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/eclipse/paho.mqtt.golang"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/go-ble/ble/linux/hci/cmd"

	"golang.org/x/net/context"

	"gopkg.in/ini.v1"
)

var sensors = map[string]string{}
var readings = map[string]map[string]int{}
var verbose = flag.Bool("verbose", false, "Enable verbose output")

func advHandler(a ble.Advertisement) {
	// 0  1  2  3  4  5  6  7  8  9  10 11 12 13 14 15 16 17
	// 50 20 AA 01 B8 92 85 DC A8 65 4C 0D 10 04 CC 00 0C 02
	if _, found := sensors[a.Addr().String()]; found &&
		len(a.ServiceData()) > 0 &&
		len(a.ServiceData()[0].Data) > 14 {
		data := a.ServiceData()[0].Data
		id := binary.LittleEndian.Uint16(data[11:])

		if readings[a.Addr().String()] == nil {
			readings[a.Addr().String()] = map[string]int{}
		}

		switch id {
		case 0x1004:
			readings[a.Addr().String()]["temp"] =
				int(binary.LittleEndian.Uint16(data[14:]))
		case 0x1006:
			readings[a.Addr().String()]["humidity"] =
				int(binary.LittleEndian.Uint16(data[14:]))
		case 0x100A:
			readings[a.Addr().String()]["battery"] =
				int(data[14])
		case 0x100D:
			readings[a.Addr().String()]["temp"] =
				int(binary.LittleEndian.Uint16(data[14:]))
			readings[a.Addr().String()]["humidity"] =
				int(binary.LittleEndian.Uint16(data[16:]))
		}
		//fmt.Printf("%s T=%d.%d H=%d.%d B=%d", comma,
		//	temp/10, temp%10,
		//	humidity/10, humidity%10, battery)
		return
	}

	if (!*verbose) {
		return
	}

	if a.Connectable() {
		fmt.Printf("[%s] C %3d:", a.Addr(), a.RSSI())
	} else {
		fmt.Printf("[%s] N %3d:", a.Addr(), a.RSSI())
	}
	comma := ""
	if len(a.LocalName()) > 0 {
		fmt.Printf(" Name: %s", a.LocalName())
		comma = ","
	}
	if len(a.Services()) > 0 {
		fmt.Printf("%s Svcs: %v", comma, a.Services())
		comma = ","
	}
	if len(a.ManufacturerData()) > 0 {
		fmt.Printf("%s MD: %X", comma, a.ManufacturerData())
	}

	fmt.Printf("\n")
}

func sensorPublish(c mqtt.Client, location string, reading map[string]int) {
	base := "collectd/mqtt.o362.us/mqtt"
	now := time.Now().Unix()

	if temp, ok := reading["temp"]; ok {
		update := fmt.Sprintf("%d:%d.%d", now, temp/10, temp%10)
		topic := fmt.Sprintf("%s/temperature-%s", base, location)
		c.Publish(topic, 0, false, update)
	}

	if humidity, ok := reading["humidity"]; ok {
		update := fmt.Sprintf("%d:%d.%d", now, humidity/10, humidity%10)
		topic := fmt.Sprintf("%s/humidity-%s", base, location)
		c.Publish(topic, 0, false, update)
	}

	if battery, ok := reading["battery"]; ok {
		update := fmt.Sprintf("%d:%d", now, battery)
		topic := fmt.Sprintf("%s/battery-%s", base, location)
		c.Publish(topic, 0, false, update)
	}
}

func main() {
	configfile := flag.String("config", "/etc/gomijia.ini", "Config file location")
	flag.Parse()

	cfg, err := ini.Load(*configfile)
	if err != nil {
		fmt.Printf("Failed to load configuration file: %v\n", err)
		os.Exit(1)
	}

	// Grab the Bluetooth LE device
	d, err := linux.NewDevice()
	if err != nil {
		print("Can't get device: " + err.Error() + "\n")
		os.Exit(1)
	}

	// Reconfigure scanning to be passive
	if err := d.HCI.Send(&cmd.LESetScanParameters{
		LEScanType:           0x00,   // 0x00: passive
		LEScanInterval:       0x4000, // 0x0004 - 0x4000; N * 0.625msec
		LEScanWindow:         0x4000, // 0x0004 - 0x4000; N * 0.625msec
		OwnAddressType:       0x00,   // 0x00: public
		ScanningFilterPolicy: 0x00,   // 0x00: accept all
	}, nil); err != nil {
		print("Can't set scan parameters: " + err.Error() + "\n")
		os.Exit(1)
	}

	sec, err := cfg.GetSection("MQTT")
	if err != nil {
		fmt.Printf("Can't find MQTT configuration section.\n")
		os.Exit(1)
	}
	if !sec.HasKey("broker") {
		fmt.Printf("Must define MQTT broker host.\n")
		os.Exit(1)
	}

	// Connect to the MQTT server
	opts := mqtt.NewClientOptions().AddBroker("ssl://" +
		sec.Key("broker").String() + ":8883")
	opts.SetClientID("gomijia")
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	if sec.HasKey("username") {
		opts.SetUsername(sec.Key("username").String())
	}
	if sec.HasKey("password") {
		opts.SetPassword(sec.Key("password").String())
	}
	c := mqtt.NewClient(opts)

	sec, err = cfg.GetSection("Devices")
	if err != nil {
		fmt.Printf("Can't find Devices configuration section.\n")
		os.Exit(1)
	}
	names := sec.KeyStrings()

	for _, name := range names {
		sensors[sec.Key(name).String()] = name
	}

	if token := c.Connect(); token.Wait() && token.Error() != nil {
		panic(token.Error())
	}

	for true {
		ctx := ble.WithSigHandler(context.WithTimeout(
			context.Background(), time.Minute))

		err = d.Scan(ctx, true, advHandler)

		if err != nil && err != context.DeadlineExceeded {
			print("Error with scan: " + err.Error() + "\n")
			os.Exit(1)
		}

		for addr, reading := range readings {
			if (*verbose) {
				fmt.Println(reading)
			}

			if location, ok := sensors[addr]; ok {
				sensorPublish(c, location, reading)
			}
			// Remove the old reading
			delete(readings, addr)
		}
	}

	c.Disconnect(250)
}
