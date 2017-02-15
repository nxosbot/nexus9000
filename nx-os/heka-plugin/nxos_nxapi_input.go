/***** BEGIN LICENSE BLOCK *****
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this file,
# You can obtain one at http://mozilla.org/MPL/2.0/.
#
# The Initial Developer of the Original Code is the Mozilla Foundation.
# Portions created by the Initial Developer are Copyright (C) 2012-2015
# the Initial Developer. All Rights Reserved.
#
# Contributor(s):
#
# ***** END LICENSE BLOCK *****/

package nxos

import (
	"fmt"
    "encoding/json"
    "bytes"
    "net/http"
    "io/ioutil"
    //"reflect"
	"strconv"
	"github.com/mozilla-services/heka/message"
	. "github.com/mozilla-services/heka/pipeline"
)

/*
type ResponseData struct {
	Time       float64
	Size       int
	StatusCode int
	Status     string
	Proto      string
	Url        string
}
*/

type NxosNxapiInput struct {
	name            string
	urls            []string
	stopChan        chan bool
	conf            *NxosNxapiInputConfig
	ir              InputRunner
	sRunners        []SplitterRunner
	hostname        string
	packSupply      chan *PipelinePack
	customUserAgent bool
}

// Http Input config struct
type NxosNxapiInputConfig struct {
	// Url for NxosNxapiInput to request.
	Url             string
	// Urls for NxosNxapiInput to request.
	Urls            []string
	// Request method. Default is "GET"
	Method          string
	// Request headers
	Headers         map[string]string
	// Request body for POST
	Body            string
	// Username and password for Basic Authentication
	Username        string
	Password        string
    // CLI command input for nxapi-cli
    Command         string
    // Property in the command output
    Prop            string
	// Default interval at which http.Get will execute. Default is 10 seconds.
	TickerInterval  uint `toml:"ticker_interval"`
	// Severity level of successful requests. Default is 6 (information)
	SuccessSeverity int32 `toml:"success_severity"`
	// Severity level of errors and unsuccessful requests. Default is 1 (alert)
	ErrorSeverity   int32 `toml:"error_severity"`
}

type InsPayload struct {
    Version         string  `json:"version"`
    CmdType         string  `json:"type"`
    Chunk           string  `json:"chunk"`
    Sid             string  `json:"sid"`
    Input           string  `json:"input"`
    OutputFormat    string  `json:"output_format"`
}

type InsMsg struct {
    Payload  InsPayload  `json:"ins_api"`
}

func (nxi *NxosNxapiInput) SetName(name string) {
	nxi.name = name
}

func (nxi *NxosNxapiInput) ConfigStruct() interface{} {
	return &NxosNxapiInputConfig{
		Method:          "GET",
		TickerInterval:  uint(10),
		SuccessSeverity: int32(6),
		ErrorSeverity:   int32(1),
	}
}

func (nxi *NxosNxapiInput) Init(config interface{}) error {
	nxi.conf = config.(*NxosNxapiInputConfig)
	if (nxi.conf.Urls == nil) && (nxi.conf.Url == "") {
		return fmt.Errorf("Url or Urls must contain at least one URL")
	}
	if nxi.conf.Urls != nil {
		nxi.urls = nxi.conf.Urls
	} else {
		nxi.urls = []string{nxi.conf.Url}
	}

	if nxi.conf.Username == "" {
		return fmt.Errorf("Username cannot be empty")
    }

	if nxi.conf.Password == "" {
		return fmt.Errorf("Password cannot be empty")
    }

	nxi.stopChan = make(chan bool)

	// Check to see if a custom user-agent is in use.
	h := make(http.Header)
	for key, value := range nxi.conf.Headers {
		h.Add(key, value)
	}
	if h.Get("User-Agent") != "" {
		nxi.customUserAgent = true
	}

	return nil
}

func (nxi *NxosNxapiInput) addField(pack *PipelinePack,
                                    name string,
                                    value interface{},
	                                representation string) {

	if field, err := message.NewField(name, value, representation); err == nil {
		pack.Message.AddField(field)
	} else {
		nxi.ir.LogError(fmt.Errorf("can't add '%s' field: %s", name, err.Error()))
	}
}

func (nxi *NxosNxapiInput) makePackDecorator(url string, prop string) func(*PipelinePack) {
    msgType := "heka.nxapi"
    if prop != "" {
        msgType = msgType + "." + prop
    }

	packDecorator := func(pack *PipelinePack) {
		pack.Message.SetType(msgType)
		pack.Message.SetHostname(nxi.hostname)
		pack.Message.SetLogger(url)
	}
	return packDecorator
}

func (nxi *NxosNxapiInput) getData (url string, command string) (map[string]interface{}, error) {
    insData := InsPayload{
                            Version: "1.0",
                            CmdType: "cli_show",
                            Chunk: "0",
                            Sid: "1",
                            Input: command,
                            OutputFormat: "json",
                         }
    insMsg := InsMsg{ Payload: insData }

    buf := new(bytes.Buffer)
    json.NewEncoder(buf).Encode(insMsg)

    client := &http.Client{}
    req, err := http.NewRequest("POST", url, buf)
    req.SetBasicAuth(nxi.conf.Username, nxi.conf.Password)

    resp, err := client.Do(req)
    if err != nil {
        nxi.ir.LogError(fmt.Errorf("Error connecting to %s", url))
        return nil, err
    }
    defer resp.Body.Close()

    body, err := ioutil.ReadAll(resp.Body)
    if err != nil {
        nxi.ir.LogError(fmt.Errorf("Error reading response data"))
        return nil, err
    }
    //fmt.Println("body=", string(body))

    var data map[string]interface{}
    if err := json.Unmarshal(body, &data); err != nil {
        nxi.ir.LogError(fmt.Errorf("Error decoding JSON"))
        return nil, err
    }

    ins := data["ins_api"].(map[string]interface{})
    outputs := ins["outputs"].(map[string]interface{})
    output := outputs["output"].(map[string]interface{})
    outBody := output["body"].(map[string]interface{})

    return outBody, nil
}

const (
    dataKey         = "output"
    hostnameCmd     = "show hostname"
    hostnameKey     = "hostname"
    hostipKey       = "hostip"
)

func (nxi *NxosNxapiInput) getHostName (url string) (string) {
    var hostname string = "na"

    output, err := nxi.getData(url, hostnameCmd)
    if err != nil {
        return hostname
    }

    if value, ok :=  output[hostnameKey]; ok {
        hostname = value.(string)
    }

    return hostname
}

func (nxi *NxosNxapiInput) fetchData (url string, sRunner SplitterRunner) {
    nxapiUrl := "http://" + url + "/ins"

    hostname := nxi.getHostName(nxapiUrl)

    output, err := nxi.getData(nxapiUrl, nxi.conf.Command)
    if err != nil {
        return
    }

    data := map[string]interface{}{ hostipKey: url, hostnameKey: hostname }
    if value, ok := output[nxi.conf.Prop]; ok {
        data[dataKey] = map[string]interface{} { nxi.conf.Prop: value }
    } else {
        data[dataKey] = output
    }
    //fmt.Println("data=", data)

    str, err := json.Marshal(data)
    if err != nil {
        nxi.ir.LogError(fmt.Errorf("Error encoding JSON"))
        return
    }

	if !sRunner.UseMsgBytes() {
		sRunner.SetPackDecorator(nxi.makePackDecorator(url, nxi.conf.Prop))
	}

	_, err = sRunner.SplitBytes(str, nil)
	if err != nil {
		nxi.ir.LogError(fmt.Errorf("fetching %s response input: %s", url, err.Error()))
	}
}

func (nxi *NxosNxapiInput) Run(ir InputRunner, h PluginHelper) (err error) {
	nxi.ir = ir
	nxi.sRunners = make([]SplitterRunner, len(nxi.urls))
	nxi.hostname = h.Hostname()

	for i, _ := range nxi.urls {
		token := strconv.Itoa(i)
		nxi.sRunners[i] = ir.NewSplitterRunner(token)
	}

	defer func() {
		for _, sRunner := range nxi.sRunners {
			sRunner.Done()
		}
	}()

	ticker := ir.Ticker()
	for {
		select {
		case <-ticker:
			for i, url := range nxi.urls {
				sRunner := nxi.sRunners[i]
				nxi.fetchData(url, sRunner)
			}
		case <-nxi.stopChan:
			return nil
		}
	}
}

func (nxi *NxosNxapiInput) Stop() {
	close(nxi.stopChan)
}

func init() {
	RegisterPlugin("NxosNxapiInput", func() interface{} {
		return new(NxosNxapiInput)
	})
}
