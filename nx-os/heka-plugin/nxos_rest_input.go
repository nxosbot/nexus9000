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
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mozilla-services/heka/message"
	. "github.com/mozilla-services/heka/pipeline"
	"io/ioutil"
	"net/http"
	"strconv"
)

type NxosRestInput struct {
	name            string
	urls            []string
	stopChan        chan bool
	conf            *NxosRestInputConfig
	ir              InputRunner
	sRunners        []SplitterRunner
	hostname        string
	packSupply      chan *PipelinePack
	customUserAgent bool
}

// Http Input config struct
type NxosRestInputConfig struct {
	// Url for NxosRestInput to request.
	Url string
	// Urls for NxosRestInput to request.
	Urls []string
	// Request method. Default is "GET"
	Method string
	// Request headers
	Headers map[string]string
	// Request body for POST
	Body string
	// Username and password for Basic Authentication
	Username string
	Password string
	// Dn and attr and mo name for target MO
	Dn   string
	Mo   string
	Prop string
	// Default interval at which http.Get will execute. Default is 10 seconds.
	TickerInterval uint `toml:"ticker_interval"`
	// Severity level of successful requests. Default is 6 (information)
	SuccessSeverity int32 `toml:"success_severity"`
	// Severity level of errors and unsuccessful requests. Default is 1 (alert)
	ErrorSeverity int32 `toml:"error_severity"`
}

type User struct {
	Name string `json:"name"`
	Pwd  string `json:"pwd"`
}

type UserAttribute struct {
	Attributes User `json:"attributes"`
}

type AaaUser struct {
	User UserAttribute `json:"aaaUser"`
}

func (nri *NxosRestInput) SetName(name string) {
	nri.name = name
}

func (nri *NxosRestInput) ConfigStruct() interface{} {
	return &NxosRestInputConfig{
		Method:          "GET",
		TickerInterval:  uint(10),
		SuccessSeverity: int32(6),
		ErrorSeverity:   int32(1),
	}
}

func (nri *NxosRestInput) Init(config interface{}) error {
	nri.conf = config.(*NxosRestInputConfig)
	if (nri.conf.Urls == nil) && (nri.conf.Url == "") {
		return fmt.Errorf("Url or Urls must contain at least one URL")
	}

	if nri.conf.Urls != nil {
		nri.urls = nri.conf.Urls
	} else {
		nri.urls = []string{nri.conf.Url}
	}

	if nri.conf.Username == "" {
		return fmt.Errorf("Username cannot be empty")
	}

	if nri.conf.Password == "" {
		return fmt.Errorf("Password cannot be empty")
	}

	nri.stopChan = make(chan bool)

	// Check to see if a custom user-agent is in use.
	h := make(http.Header)
	for key, value := range nri.conf.Headers {
		h.Add(key, value)
	}

	if h.Get("User-Agent") != "" {
		nri.customUserAgent = true
	}

	return nil
}

func (nri *NxosRestInput) addField(pack *PipelinePack,
	name string,
	value interface{},
	representation string) {

	if field, err := message.NewField(name, value, representation); err == nil {
		pack.Message.AddField(field)
	} else {
		nri.ir.LogError(fmt.Errorf("can't add '%s' field: %s", name, err.Error()))
	}
}

func (nri *NxosRestInput) makePackDecorator(url string) func(*PipelinePack) {
	packDecorator := func(pack *PipelinePack) {
		pack.Message.SetType("heka.nxosrest")
		pack.Message.SetHostname(nri.hostname)
		pack.Message.SetLogger(url)
	}
	return packDecorator
}

func (nri *NxosRestInput) getToken(url string) (string, error) {
	user := User{Name: nri.conf.Username, Pwd: nri.conf.Password}
	userAttr := UserAttribute{Attributes: user}
	userData := AaaUser{User: userAttr}

	buf := new(bytes.Buffer)
	json.NewEncoder(buf).Encode(userData)

	resp, err := http.Post(url, "application/json", buf)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("Error connecting to %s", url))
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("Error reading response data"))
		return "", err
	}
	//fmt.Println("body=", string(body))

	var data map[string]interface{}

	if err := json.Unmarshal(body, &data); err != nil {
		nri.ir.LogError(fmt.Errorf("Error decoding JSON"))
		return "", err
	}

	imdata := data["imdata"].([]interface{})[0]
	login := imdata.(map[string]interface{})["aaaLogin"]
	attr := login.(map[string]interface{})["attributes"]
	token := attr.(map[string]interface{})["token"].(string)
	//fmt.Println("token=", token)

	return token, nil
}

func (nri *NxosRestInput) getMo(url string, token string) (interface{}, error) {
	client := &http.Client{}

	url = url + "/" + nri.conf.Dn + ".json"
	req, err := http.NewRequest("GET", url, nil)

	tokenCookie := "APIC-Cookie=" + token
	//fmt.Println("cookie=", tokenCookie)
	req.Header.Set("Cookie", tokenCookie)

	resp, err := client.Do(req)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("Error connecting to %s", url))
		return "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("Error reading response data"))
		return "", err
	}
	defer resp.Body.Close()

	//fmt.Println("body=", string(body));

	var data map[string]interface{}

	if err := json.Unmarshal(body, &data); err != nil {
		nri.ir.LogError(fmt.Errorf("Error decoding JSON"))
		return "", err
	}

	var attr interface{}
	if len(data["imdata"].([]interface{})) > 0 {
		imdata := data["imdata"].([]interface{})[0]
		mo := imdata.(map[string]interface{})[nri.conf.Mo]
		attr = mo.(map[string]interface{})["attributes"]
	}

	return attr, nil
}

func (nri *NxosRestInput) fetchData(url string, sRunner SplitterRunner) {
	baseUrl := "http://" + url + "/api"

	token, err := nri.getToken(baseUrl + "/aaaLogin.json")
	if err != nil {
		return
	}
	//fmt.Println("token=", token)

	mo, err := nri.getMo(baseUrl+"/mo", token)
	if err != nil {
		return
	}

	var data map[string]interface{}
	if value, ok := mo.(map[string]interface{})[nri.conf.Prop]; ok {
		data = map[string]interface{}{
			nri.conf.Mo: map[string]interface{}{
				nri.conf.Prop: value,
			},
		}
	} else {
		data = map[string]interface{}{nri.conf.Mo: mo}
	}

	str, err := json.Marshal(data)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("Error encoding JSON"))
		return
	}

	if !sRunner.UseMsgBytes() {
		sRunner.SetPackDecorator(nri.makePackDecorator(url))
	}

	_, err = sRunner.SplitBytes(str, nil)
	if err != nil {
		nri.ir.LogError(fmt.Errorf("fetching %s response input: %s", url, err.Error()))
	}
}

func (nri *NxosRestInput) Run(ir InputRunner, h PluginHelper) (err error) {
	nri.ir = ir
	nri.sRunners = make([]SplitterRunner, len(nri.urls))
	nri.hostname = h.Hostname()

	for i, _ := range nri.urls {
		token := strconv.Itoa(i)
		nri.sRunners[i] = ir.NewSplitterRunner(token)
	}

	defer func() {
		for _, sRunner := range nri.sRunners {
			sRunner.Done()
		}
	}()

	ticker := ir.Ticker()
	for {
		select {
		case <-ticker:
			for i, url := range nri.urls {
				sRunner := nri.sRunners[i]
				nri.fetchData(url, sRunner)
			}
		case <-nri.stopChan:
			return nil
		}
	}
}

func (nri *NxosRestInput) Stop() {
	close(nri.stopChan)
}

func init() {
	RegisterPlugin("NxosRestInput", func() interface{} {
		return new(NxosRestInput)
	})
}
