/*
Copyright 2019 VMware, Inc

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"time"
)

type Event struct {
	Timestamp time.Time
	Name      string
	Data      string
}

type EventFormatter interface {
	Preamble() string
	Epilogue() string
	Format(event *Event) string
}

type DefaultFormatter struct{}

func (_ DefaultFormatter) Preamble() string {
	return ""
}

func (_ DefaultFormatter) Epilogue() string {
	return ""
}

func (_ DefaultFormatter) Format(event *Event) string {
	const timeFormat = "2006-01-02 15:04:05.000"
	return fmt.Sprintf("[%s] %s\n%s\n", event.Timestamp.Format(timeFormat), event.Name, event.Data)
}

type TraceEventFormatter struct{}

func (_ TraceEventFormatter) Preamble() string {
	return "[\n"
}

func (_ TraceEventFormatter) Epilogue() string {
	return "]\n"
}

func (_ TraceEventFormatter) Format(event *Event) string {
	return fmt.Sprintf(`{"ts": %f, "name": %q, "ph": "i", "pid": 1, "tid": 1, "s": "t", "args": [%q]},
`, float64(event.Timestamp.UnixNano())/1000, event.Name, event.Data)
}
