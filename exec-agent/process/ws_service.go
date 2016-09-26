package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/eclipse/che/exec-agent/op"
	"math"
	"time"
)

const (
	ProcessStartOp            = "process.start"
	ProcessKillOp             = "process.kill"
	ProcessSubscribeOp        = "process.subscribe"
	ProcessUnsubscribeOp      = "process.unsubscribe"
	ProcessUpdateSubscriberOp = "process.updateSubscriber"
	ProcessGetLogsOp          = "process.getLogs"

	NoSuchProcessErrorCode = -32000
)

var OpRoutes = op.RoutesGroup{
	"Process Routes",
	[]op.Route{
		{
			ProcessStartOp,
			func(body []byte) (interface{}, error) {
				b := StartParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			startProcessCallHF,
		},
		{
			ProcessKillOp,
			func(body []byte) (interface{}, error) {
				b := KillParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			killProcessCallHF,
		},
		{
			ProcessSubscribeOp,
			func(body []byte) (interface{}, error) {
				b := SubscribeParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			subscribeCallHF,
		},
		{
			ProcessUnsubscribeOp,
			func(body []byte) (interface{}, error) {
				b := UnsubscribeParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			unsubscribeCallHF,
		},
		{
			ProcessUpdateSubscriberOp,
			func(body []byte) (interface{}, error) {
				b := UpdateSubscriberParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			updateSubscriberCallHF,
		},
		{
			ProcessGetLogsOp,
			func(body []byte) (interface{}, error) {
				b := GetLogsParams{}
				err := json.Unmarshal(body, &b)
				return b, err
			},
			getProcessLogsCallHF,
		},
	},
}

type StartParams struct {
	Name        string `json:"name"`
	CommandLine string `json:"commandLine"`
	Type        string `json:"type"`
	EventTypes  string `json:"eventTypes"`
}

type KillParams struct {
	Pid       uint64 `json:"pid"`
	NativePid uint64 `json:"nativePid"`
}

type SubscribeParams struct {
	Pid        uint64 `json:"pid"`
	EventTypes string `json:"eventTypes"`
	After      string `json:"after"`
}

type SubscribeResult struct {
	Pid        uint64 `json:"pid"`
	EventTypes string `json:"eventTypes"`
	Text       string `json:"text"`
}

type UnsubscribeParams struct {
	Pid uint64 `json:"pid"`
}

type UpdateSubscriberParams struct {
	Pid        uint64 `json:"pid"`
	EventTypes string `json:"eventTypes"`
}

type ProcessResult struct {
	Pid  uint64 `json:"pid"`
	Text string `json:"text"`
}

type GetLogsParams struct {
	Pid   uint64 `json:"pid"`
	From  string `json:"from"`
	Till  string `json:"till"`
	Limit int    `json:"limit"`
	Skip  int    `json:"skip"`
}

func startProcessCallHF(body interface{}, t *op.Transmitter) error {
	startBody := body.(StartParams)

	// Creating command
	command := Command{
		Name:        startBody.Name,
		CommandLine: startBody.CommandLine,
		Type:        startBody.Type,
	}
	if err := checkCommand(&command); err != nil {
		return op.NewArgsError(err)
	}

	// Detecting subscription mask
	subscriber := &Subscriber{
		Id:      t.Channel.Id,
		Mask:    parseTypes(startBody.EventTypes),
		Channel: t.Channel.Events,
	}

	process := NewProcess(command).BeforeEventsHook(func(process *MachineProcess) {
		t.Send(process)
	})
	if subscriber != nil {
		if err := process.AddSubscriber(subscriber); err != nil {
			return err
		}
	}

	return process.Start()
}

func killProcessCallHF(body interface{}, t *op.Transmitter) error {
	killBody := body.(KillParams)
	p, ok := Get(killBody.Pid)
	if !ok {
		return newNoSuchProcessError(killBody.Pid)
	}
	if err := p.Kill(); err != nil {
		return err
	}
	t.Send(&ProcessResult{
		Pid:  killBody.Pid,
		Text: "Successfully killed",
	})
	return nil
}

func subscribeCallHF(body interface{}, t *op.Transmitter) error {
	subscribeBody := body.(SubscribeParams)
	p, ok := Get(subscribeBody.Pid)
	if !ok {
		return newNoSuchProcessError(subscribeBody.Pid)
	}

	subscriber := &Subscriber{
		Id:      t.Channel.Id,
		Mask:    parseTypes(subscribeBody.EventTypes),
		Channel: t.Channel.Events,
	}

	// Check whether subscriber should see previous logs or not
	if subscribeBody.After == "" {
		if err := p.AddSubscriber(subscriber); err != nil {
			return err
		}
	} else {
		after, err := time.Parse(DateTimeFormat, subscribeBody.After)
		if err != nil {
			return op.NewArgsError(errors.New("Bad format of 'after', " + err.Error()))
		}
		if err := p.RestoreSubscriber(subscriber, after); err != nil {
			return err
		}
	}
	t.Send(&SubscribeResult{
		Pid:        p.Pid,
		EventTypes: subscribeBody.EventTypes,
		Text:       "Successfully subscribed",
	})
	return nil
}

func unsubscribeCallHF(call interface{}, t *op.Transmitter) error {
	unsubscribeBody := call.(UnsubscribeParams)
	p, ok := Get(unsubscribeBody.Pid)
	if !ok {
		return errors.New(fmt.Sprintf("Process with id '%s' doesn't exist", unsubscribeBody.Pid))
	}
	p.RemoveSubscriber(t.Channel.Id)
	t.Send(&ProcessResult{
		Pid:  p.Pid,
		Text: "Successfully unsubscribed",
	})
	return nil
}

func updateSubscriberCallHF(body interface{}, t *op.Transmitter) error {
	updateBody := body.(UpdateSubscriberParams)
	p, ok := Get(updateBody.Pid)
	if !ok {
		return newNoSuchProcessError(updateBody.Pid)
	}
	if updateBody.EventTypes == "" {
		return op.NewArgsError(errors.New("'eventTypes' required for subscriber update"))
	}

	if err := p.UpdateSubscriber(t.Channel.Id, maskFromTypes(updateBody.EventTypes)); err != nil {
		return err
	}
	t.Send(&SubscribeResult{
		Pid:        p.Pid,
		EventTypes: updateBody.EventTypes,
		Text:       "Subscriber successfully updated",
	})
	return nil
}

func getProcessLogsCallHF(body interface{}, t *op.Transmitter) error {
	args := body.(GetLogsParams)
	p, ok := Get(args.Pid)
	if !ok {
		return newNoSuchProcessError(args.Pid)
	}

	from, err := parseTime(args.From, time.Time{})
	if err != nil {
		return op.NewArgsError(errors.New("Bad format of 'from', " + err.Error()))
	}

	till, err := parseTime(args.Till, time.Now())
	if err != nil {
		return op.NewArgsError(errors.New("Bad format of 'till', " + err.Error()))
	}

	logs, err := p.ReadLogs(from, till)
	if err != nil {
		return err
	}

	limit := DefaultLogsPerPageLimit
	if args.Limit != 0 {
		if limit < 1 {
			return op.NewArgsError(errors.New("Required 'limit' to be > 0"))
		}
		limit = args.Limit
	}

	skip := 0
	if args.Skip != 0 {
		if skip < 0 {
			return op.NewArgsError(errors.New("Required 'skip' to be >= 0"))
		}
		skip = args.Skip
	}

	len := len(logs)
	fromIdx := int(math.Max(float64(len-limit-skip), 0))
	toIdx := len - int(math.Min(float64(skip), float64(len)))

	t.Send(logs[fromIdx:toIdx])
	return nil
}

func newNoSuchProcessError(pid uint64) op.Error {
	return op.NewError(errors.New(fmt.Sprintf("No process with id '%d'", pid)), NoSuchProcessErrorCode)
}
