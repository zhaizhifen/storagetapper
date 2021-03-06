// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package snapshot

import (
	"database/sql"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/uber/storagetapper/config"
	"github.com/uber/storagetapper/db"
	"github.com/uber/storagetapper/encoder"
	"github.com/uber/storagetapper/log"
	"github.com/uber/storagetapper/state"
	"github.com/uber/storagetapper/test"
	"github.com/uber/storagetapper/types"
	"github.com/uber/storagetapper/util"
)

var cfg *config.AppConfig

func ExecSQL(conn *sql.DB, t *testing.T, query string, param ...interface{}) {
	test.CheckFail(util.ExecSQL(conn, query, param...), t)
}

func createDB(a db.Addr, t *testing.T) {
	a.Db = ""
	conn, err := db.Open(&a)
	test.CheckFail(err, t)
	defer func() {
		err := conn.Close()
		test.CheckFail(err, t)
	}()

	ExecSQL(conn, t, "drop database if exists snap_test_db1")
	ExecSQL(conn, t, "create database snap_test_db1")
	ExecSQL(conn, t, "create table snap_test_db1.snap_test_t1 ( f1 int not null primary key, f2 varchar(32), f3 double)")

	state.DeregisterTable("snap_test_svc1", "snap_test_db1", "snap_test_t1")

	if !state.RegisterTable(&db.Loc{Service: "snap_test_svc1", Name: "snap_test_db1"}, "snap_test_t1", "mysql", "") {
		t.FailNow()
	}
}

func TestEmptyTable(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	if !state.Init(cfg) {
		log.Fatalf("State init failed")
	}
	defer func() { test.CheckFail(state.Close(), t) }()

	ci := db.GetInfo(&db.Loc{Service: "snap_test_svc1", Name: "snap_test_db1"}, db.Slave)
	createDB(*ci, t)

	conn, err := db.Open(ci)
	test.CheckFail(err, t)
	defer func() {
		err := conn.Close()
		test.CheckFail(err, t)
	}()

	s := Reader{}
	var enc encoder.Encoder
	if encoder.Internal.Type() == "msgpack" {
		enc, err = encoder.Create("msgpack", "snap_test_svc1", "snap_test_db1", "snap_test_t1")
	} else if encoder.Internal.Type() == "json" {
		enc, err = encoder.Create("json", "snap_test_svc1", "snap_test_db1", "snap_test_t1")
	}

	test.CheckFail(err, t)

	_, err = s.Prepare("snap_test_cluster1", "snap_test_svc1", "snap_test_db1", "snap_test_t1", enc)
	test.CheckFail(err, t)
	defer s.End()

	for s.HasNext() {
		_, _, err := s.GetNext()
		if err != nil {
			test.CheckFail(err, t)
		}
	}
}

func TestBasic(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	if !state.Init(cfg) {
		log.Fatalf("State init failed")
	}
	defer func() { test.CheckFail(state.Close(), t) }()

	ci := db.GetInfo(&db.Loc{Service: "snap_test_svc1", Name: "snap_test_db1"}, db.Slave)
	createDB(*ci, t)

	conn, err := db.Open(ci)
	test.CheckFail(err, t)
	defer func() {
		err := conn.Close()
		test.CheckFail(err, t)
	}()

	for i := 0; i < 1000; i++ {
		ExecSQL(conn, t, "insert into snap_test_t1 values(?,?,?)", i, strconv.Itoa(i), float64(i)/3)
	}

	s := Reader{}
	var enc encoder.Encoder
	enc, err = encoder.Create(encoder.Internal.Type(), "snap_test_svc1", "snap_test_db1", "snap_test_t1")

	test.CheckFail(err, t)

	_, err = s.Prepare("snap_test_cluster1", "snap_test_svc1", "snap_test_db1", "snap_test_t1", enc)
	test.CheckFail(err, t)
	defer s.End()

	var i int64
	for s.HasNext() {
		key, data, err := s.GetNext()
		if err != nil {
			test.CheckFail(err, t)
		} else {
			cf, err := encoder.Internal.DecodeEvent(data)
			cf.Timestamp = 0
			test.CheckFail(err, t)
			refcf := types.CommonFormatEvent{Type: "insert", Key: []interface{}{float64(i)}, SeqNo: 0, Timestamp: 0, Fields: &[]types.CommonFormatField{{Name: "f1", Value: float64(i)}, {Name: "f2", Value: strconv.FormatInt(i, 10)}, {Name: "f3", Value: float64(i) / 3}}}
			// refcf := types.CommonFormatEvent{Type: "insert", Key: []interface{}{i}, SeqNo: 0, Timestamp: 0, Fields: &[]types.CommonFormatField{{Name: "f1", Value: i}, {Name: "f2", Value: strconv.FormatInt(i, 10)}, {Name: "f3", Value: float64(i) / 3}}}

			ChangeCfFields(cf)
			if !reflect.DeepEqual(&refcf, cf) {
				log.Errorf("Received: %+v %+v", cf, cf.Fields)
				log.Errorf("Reference: %+v %+v", &refcf, refcf.Fields)
				t.FailNow()
			}
			if key != encoder.GetCommonFormatKey(cf) {
				log.Errorf("Received: %+v", key)
				log.Errorf("Reference: %+v", encoder.GetCommonFormatKey(cf))
				t.FailNow()
			}
			i++
		}
	}

}

func TestMoreFieldTypes(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	if !state.Init(cfg) {
		log.Fatalf("State init failed")
	}
	defer func() { test.CheckFail(state.Close(), t) }()

	ci := db.GetInfo(&db.Loc{Service: "snap_test_svc1", Name: "snap_test_db1"}, db.Slave)
	createDB(*ci, t)

	conn, err := db.Open(ci)
	test.CheckFail(err, t)
	defer func() {
		err := conn.Close()
		test.CheckFail(err, t)
	}()

	for i := 0; i < 1; i++ {
		ExecSQL(conn, t, "insert into snap_test_t1(f1) values(?)", i)
	}

	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f4 text")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f5 timestamp")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f6 date")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f7 time")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f8 year")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f9 bigint")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f10 binary")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f11 int")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f12 float")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f13 double")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f14 decimal")
	ExecSQL(conn, t, "ALTER TABLE snap_test_t1 add f15 numeric")

	//msgpack doesn't preserve int size, so all int32 became int64
	expectedType := []string{
		"int64",
		"string",
		"float64",
		"[]uint8",
		"string",
		"string",
		"string",
		"int64", //int32
		"int64",
		"[]uint8",
		"int64", //int32
		"float32",
		"float64",
		"float64",
		"float64",
	}

	ExecSQL(conn, t, "insert into snap_test_t1 values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", 1567, strconv.Itoa(1567), float64(1567)/3, "testtextfield", time.Now(), time.Now(), time.Now(), time.Now(), 98878, []byte("testbinaryfield"), 827738, 111.23, 222.34, 333.45, 444.56)

	if !state.DeregisterTable("snap_test_svc1", "snap_test_db1", "snap_test_t1") {
		t.FailNow()
	}

	if !state.RegisterTable(&db.Loc{Service: "snap_test_svc1", Name: "snap_test_db1"}, "snap_test_t1", "mysql", "") {
		t.FailNow()
	}

	s := Reader{}
	var enc encoder.Encoder
	enc, err = encoder.Create("msgpack", "snap_test_svc1", "snap_test_db1", "snap_test_t1")
	test.CheckFail(err, t)

	_, err = s.Prepare("snap_test_cluster1", "snap_test_svc1", "snap_test_db1", "snap_test_t1", enc)
	test.CheckFail(err, t)
	defer s.End()

	for s.HasNext() {
		_, msg, err := s.GetNext()
		test.CheckFail(err, t)
		d, err := enc.DecodeEvent(msg)
		test.CheckFail(err, t)
		sch := enc.Schema()
		for i, v := range *d.Fields {
			if v.Value != nil && reflect.TypeOf(v.Value).String() != expectedType[i] {
				log.Errorf("%v: got: %v expected: %v %v %v", v.Name, reflect.TypeOf(v.Value).String(), expectedType[i], sch.Columns[i].DataType, sch.Columns[i].Type)
				t.FailNow()
			}
		}
	}
}

func ChangeCfFields(cf *types.CommonFormatEvent) {
	switch (cf.Key[0]).(type) {
	case int64:
		cf.Key[0] = float64(cf.Key[0].(int64))
	case uint64:
		cf.Key[0] = float64(cf.Key[0].(uint64))
	}

	if cf.Fields != nil {
		for f := range *cf.Fields {
			switch ((*cf.Fields)[f].Value).(type) {
			case int64:
				val := ((*cf.Fields)[f].Value).(int64)
				newFloat := float64(val)
				(*cf.Fields)[f].Value = newFloat
			case uint64:
				val := ((*cf.Fields)[f].Value).(uint64)
				newFloat := float64(val)
				(*cf.Fields)[f].Value = newFloat
			}
		}
	}
}

func TestSnapshotConsistency(t *testing.T) {
	test.SkipIfNoMySQLAvailable(t)

	if !state.Init(cfg) {
		log.Fatalf("State init failed")
	}
	defer func() { test.CheckFail(state.Close(), t) }()

	ci := db.GetInfo(&db.Loc{Cluster: "snap_test_cluster1", Service: "snap_test_svc1", Name: "snap_test_db1"}, db.Slave)
	createDB(*ci, t)

	conn, err := db.Open(ci)
	test.CheckFail(err, t)
	defer func() {
		err := conn.Close()
		test.CheckFail(err, t)
	}()

	for i := 0; i < 1000; i++ {
		ExecSQL(conn, t, "insert into snap_test_t1 values(?,?,?)", i, strconv.Itoa(i), float64(i)/3)
	}

	/* Make some gaps */
	ExecSQL(conn, t, "delete from snap_test_t1 where f1 > 700 && f1 < 800")
	ExecSQL(conn, t, "delete from snap_test_t1 where f1 > 300 && f1 < 400")

	s := Reader{}
	var enc encoder.Encoder
	enc, err = encoder.Create(encoder.Internal.Type(), "snap_test_svc1", "snap_test_db1", "snap_test_t1")

	test.CheckFail(err, t)
	_, err = s.Prepare("snap_test_cluster1", "snap_test_svc1", "snap_test_db1", "snap_test_t1", enc)
	test.CheckFail(err, t)

	var i int64
	for s.HasNext() {
		key, data, err := s.GetNext()
		if err != nil {
			test.CheckFail(err, t)
		} else {
			cf, err := encoder.Internal.DecodeEvent(data)
			// cf, err := encoder.CommonFormatDecode(data)
			test.CheckFail(err, t)
			cf.Timestamp = 0
			refcf := types.CommonFormatEvent{Type: "insert", Key: []interface{}{float64(i)}, SeqNo: 0, Timestamp: 0, Fields: &[]types.CommonFormatField{{Name: "f1", Value: float64(i)}, {Name: "f2", Value: strconv.FormatInt(i, 10)}, {Name: "f3", Value: float64(i) / 3}}}
			// refcf := types.CommonFormatEvent{Type: "insert", Key: []interface{}{i}, SeqNo: 0, Timestamp: 0, Fields: &[]types.CommonFormatField{{Name: "f1", Value: i}, {Name: "f2", Value: strconv.FormatInt(i, 10)}, {Name: "f3", Value: float64(i) / 3}}}

			ChangeCfFields(cf)

			if !reflect.DeepEqual(&refcf, cf) {
				log.Errorf("Received: %+v %+v", cf, cf.Fields)
				log.Errorf("Reference: %+v %+v", &refcf, refcf.Fields)
				t.FailNow()
			}
			if key != encoder.GetCommonFormatKey(cf) {
				log.Errorf("Received: %+v", key)
				log.Errorf("Reference: %+v", encoder.GetCommonFormatKey(cf))
				t.FailNow()
			}
			if i == 300 {
				i = 400
			} else if i == 700 {
				/* Insert/delete something in the gap we already left in the past */
				ExecSQL(conn, t, "insert into snap_test_t1 values(?,?,?)", 350, "350", 350.0/3)
				ExecSQL(conn, t, "delete from snap_test_t1 where f1 > 100 && f1 < 200")
				/* Insert/delete data in the future gap, we shouldn't see it */
				ExecSQL(conn, t, "insert into snap_test_t1 values(?,?,?)", 750, "750", 750.0/3)
				ExecSQL(conn, t, "delete from snap_test_t1 where f1 > 850 && f1 < 900")
				ExecSQL(conn, t, "update snap_test_t1 set f2='bbbb' where f1 > 950 && f1 < 970")
				i = 800
			} else {
				i++
			}
		}
	}

	s.End()
}

func TestMain(m *testing.M) {
	cfg = test.LoadConfig()
	os.Exit(m.Run())
}
