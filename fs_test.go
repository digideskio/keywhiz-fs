// +build !race

// Copyright 2015 Square Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

const _SomeUID uint32 = 12345

var fuseContext = &fuse.Context{Owner: fuse.Owner{Uid: 0, Gid: 0}}

type FsTestSuite struct {
	suite.Suite
	url    *url.URL
	assert *assert.Assertions
	fs     *KeywhizFs
}

func (suite *FsTestSuite) SetupTest() {
	timeouts := Timeouts{0, 10 * time.Millisecond, 20 * time.Millisecond, 1 * time.Hour}
	metricsHandle := setupMetrics(metricsURL, metricsPrefix, *mountpoint)
	client := NewClient(clientFile, clientFile, testCaFile, suite.url, timeouts.MaxWait, logConfig, metricsHandle)
	ownership := Ownership{Uid: _SomeUID, Gid: _SomeUID}
	kwfs, _, _ := NewKeywhizFs(&client, ownership, timeouts, metricsHandle, logConfig)
	suite.fs = kwfs
}

func (suite *FsTestSuite) TestSpecialFileAttrs() {
	assert := suite.assert

	cases := []struct {
		filename string
		size     int
		mode     int
		openable bool
	}{
		{"", 4096, 0755 | fuse.S_IFDIR, false},
		{".version", len(fsVersion), 0444 | fuse.S_IFREG, true},
		{".json/status", len(suite.fs.statusJSON()), 0444 | fuse.S_IFREG, true},
		{".running", -1, 0444 | fuse.S_IFREG, true},
		{".clear_cache", 0, 0440 | fuse.S_IFREG, false},
		{".json", 4096, 0700 | fuse.S_IFDIR, false},
		{".pprof", 4096, 0700 | fuse.S_IFDIR, false},
		{".json/secret", 4096, 0700 | fuse.S_IFDIR, false},
		{".json/secrets", -1, 0400 | fuse.S_IFREG, true},
	}

	for _, c := range cases {
		attr, status := suite.fs.GetAttr(c.filename, nil)
		assert.Equal(fuse.OK, status, "Expected %v attr status to be fuse.OK", c.filename)
		assert.EqualValues(c.mode, attr.Mode, "Expected %v mode %#o, was %#o", c.filename, c.mode, attr.Mode)
		if c.size >= 0 {
			assert.EqualValues(c.size, attr.Size, "Expected %v size %d, was %d", c.filename, c.size, attr.Size)
		}
		if c.openable {
			file, status := suite.fs.Open(c.filename, 0, fuseContext)
			assert.Equal(fuse.OK, status, "Expected %v open status to be fuse.OK", c.filename)
			var fattr *fuse.Attr = new(fuse.Attr)
			status = file.GetAttr(fattr)
			assert.Equal(fuse.OK, status, "Expected %v fattr to be fuse.OK", c.filename)
			assert.EqualValues(attr, fattr, "Expected stat == fstat")
		}
	}

	attr, status := suite.fs.GetAttr("invalid", nil)
	assert.Nil(attr)
	assert.EqualValues(fuse.ENOENT, status, "Invalid file attr should give ENOENT")
}

func (suite *FsTestSuite) TestFileAttrs() {
	assert := suite.assert

	nobodySecretData := fixture("secret.json")
	nobodySecret, _ := ParseSecret(nobodySecretData)
	hmacSecretData := fixture("secretNormalOwner.json")
	hmacSecret, _ := ParseSecret(hmacSecretData)
	secretListData := fixture("secrets.json")

	cases := []struct {
		filename string
		content  []byte
		mode     uint32
	}{
		{"hmac.key", hmacSecret.Content, 0440 | fuse.S_IFREG},
		{"Nobody_PgPass", nobodySecret.Content, 0400 | fuse.S_IFREG},
		{".json/secret/hmac.key", hmacSecretData, 0400 | fuse.S_IFREG},
		{".json/secret/Nobody_PgPass", nobodySecretData, 0400 | fuse.S_IFREG},
		{".json/secrets", secretListData, 0400 | fuse.S_IFREG},
	}

	for _, c := range cases {
		attr, status := suite.fs.GetAttr(c.filename, fuseContext)
		assert.Equal(fuse.OK, status, "Expected %v attr status to be fuse.OK", c.filename)
		assert.Equal(c.mode, attr.Mode, "Expected %v mode %#o, was %#o", c.filename, c.mode, attr.Mode)
		assert.Equal(uint64(len(c.content)), attr.Size, "Expected %v size to match", c.filename)
		file, status := suite.fs.Open(c.filename, 0, fuseContext)
		assert.Equal(fuse.OK, status, "Expected %v open status to be fuse.OK", c.filename)
		var fattr *fuse.Attr = new(fuse.Attr)
		status = file.GetAttr(fattr)
		assert.Equal(fuse.OK, status, "Expected fstat to be fuse.OK")
		assert.EqualValues(attr, fattr, "Expected stat == fstat")
	}
}

func (suite *FsTestSuite) TestFileAttrOwnership() {
	assert := suite.assert

	cases := []string{
		".clear_cache",
		".json/secret/hmac.key",
		".json/secrets",
		".running",
		".version",
		".json/status",
		"hmac.key",
	}

	for _, filename := range cases {
		attr, status := suite.fs.GetAttr(filename, fuseContext)
		assert.Equal(fuse.OK, status, "Expected %v attr status to be fuse.OK", filename)
		assert.Equal(_SomeUID, attr.Uid, "Expected %v uid to be default", filename)
		assert.Equal(_SomeUID, attr.Gid, "Expected %v gid to be default", filename)
	}

	filename := "Nobody_PgPass"
	attr, status := suite.fs.GetAttr(filename, fuseContext)
	assert.Equal(fuse.OK, status, "Expected %v attr status to be fuse.OK", filename)
	assert.NotEqual(_SomeUID, attr.Uid, "Expected %v uid to not be default", filename)
	assert.NotEqual(0, attr.Uid, "Expected %v uid to be set", filename)
	assert.NotEqual(_SomeUID, attr.Gid, "Expected %v gid to not be default", filename)
	assert.NotEqual(0, attr.Gid, "Expected %v gid to be set", filename)
}

func (suite *FsTestSuite) TestSpecialFileOpen() {
	assert := suite.assert

	read := func(f nodefs.File) []byte {
		buf := make([]byte, 4000)
		res, _ := f.Read(buf, 0)
		bytes, _ := res.Bytes(buf)
		return bytes
	}

	file, status := suite.fs.Open(".version", 0, fuseContext)
	assert.Equal(fuse.OK, status)
	assert.EqualValues(fsVersion, read(file))

	file, status = suite.fs.Open(".json/status", 0, fuseContext)
	assert.Equal(fuse.OK, status)
	assert.EqualValues(suite.fs.statusJSON(), read(file))

	file, status = suite.fs.Open(".clear_cache", 0, fuseContext)
	assert.Equal(fuse.OK, status)
	assert.Empty(read(file))

	file, status = suite.fs.Open(".running", 0, fuseContext)
	assert.Equal(fuse.OK, status)
	assert.Contains(string(read(file)), "pid=")
}

func (suite *FsTestSuite) TestOpen() {
	assert := suite.assert

	nobodySecretData := fixture("secret.json")
	nobodySecret, _ := ParseSecret(nobodySecretData)
	hmacSecretData := fixture("secretNormalOwner.json")
	hmacSecret, _ := ParseSecret(hmacSecretData)
	secretListData := fixture("secrets.json")

	read := func(f nodefs.File) []byte {
		buf := make([]byte, 4000)
		res, _ := f.Read(buf, 0)
		bytes, _ := res.Bytes(buf)
		return bytes
	}

	cases := []struct {
		filename string
		content  []byte
	}{
		{"hmac.key", hmacSecret.Content},
		{"Nobody_PgPass", nobodySecret.Content},
		{".json/secret/hmac.key", hmacSecretData},
		{".json/secret/Nobody_PgPass", nobodySecretData},
		{".json/secrets", secretListData},
	}

	for _, c := range cases {
		file, status := suite.fs.Open(c.filename, 0, fuseContext)
		assert.Equal(fuse.OK, status, "Expected %v open status to be fuse.OK", c.filename)
		assert.Equal(c.content, read(file), "Expected %v file content to match", c.filename)
	}
}

func (suite *FsTestSuite) TestOpenBadFiles() {
	assert := suite.assert

	cases := []struct {
		filename string
		status   fuse.Status
	}{
		{"", fuseEISDIR},
		{"non-existent", fuse.ENOENT},
		{".json/secret/non-existent", fuse.ENOENT},
		{".json/secret", fuseEISDIR},
	}

	for _, c := range cases {
		_, status := suite.fs.Open(c.filename, 0, fuseContext)
		assert.Equal(c.status, status, "Expected %v open status to match", c.filename)
	}
}

func (suite *FsTestSuite) TestOpenDir() {
	assert := suite.assert

	cases := []struct {
		directory string
		contents  map[string]bool // name -> isFile?
	}{
		{
			"",
			map[string]bool{
				".version":     true,
				".running":     true,
				".clear_cache": true,
				".json":        false,
				".pprof":       false,
				"General_Password..0be68f903f8b7d86": true,
				"Nobody_PgPass":                      true,
			},
		},
		{
			".json",
			map[string]bool{
				"metrics":       true,
				"status":        true,
				"server_status": true,
				"secret":        false,
				"secrets":       true,
			},
		},
		{
			".json/secret",
			map[string]bool{
				"General_Password..0be68f903f8b7d86": true,
				"Nobody_PgPass":                      true,
			},
		},
	}

	for _, c := range cases {
		fsEntries, status := suite.fs.OpenDir(c.directory, fuseContext)
		assert.Equal(fuse.OK, status)
		assert.Len(fsEntries, len(c.contents))

		for _, fsEntry := range fsEntries {
			expectedIsFile, ok := c.contents[fsEntry.Name]
			assert.True(ok)
			assert.Equal(expectedIsFile, fsEntry.Mode&fuse.S_IFREG == fuse.S_IFREG)
		}
	}

	fsEntries, status := suite.fs.OpenDir("invalid", fuseContext)
	assert.Nil(fsEntries)
	assert.Equal(fuse.ENOENT, status, "Invalid directory should give ENOENT")
}

func TestFsTestSuite(t *testing.T) {
	// Starts a server for the duration of the test
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/secrets"):
			fmt.Fprint(w, string(fixture("secrets.json")))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/secret/hmac.key"):
			fmt.Fprint(w, string(fixture("secretNormalOwner.json")))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/secret/Nobody_PgPass"):
			fmt.Fprint(w, string(fixture("secret.json")))
		default:
			w.WriteHeader(404)
		}
	}))
	server.TLS = testCerts(testCaFile)
	server.StartTLS()
	defer server.Close()

	fsSuite := new(FsTestSuite)
	serverURL, _ := url.Parse(server.URL)
	fsSuite.url = serverURL
	fsSuite.assert = assert.New(t)

	suite.Run(t, fsSuite)
}

func (suite *FsTestSuite) TestUnlink() {
	assert := suite.assert
	status := suite.fs.Unlink("invalid", fuseContext)
	assert.Equal(fuse.EACCES, status, "Invalid unlink should give EACCES")

	suite.fs.Cache.Add(Secret{Name: "test"})
	status = suite.fs.Unlink(".clear_cache", fuseContext)
	assert.Equal(fuse.OK, status, "Unlink on .clear_cache should give OK")
	assert.Equal(suite.fs.Cache.Len(), 0, "Should clear cache")
}

func (suite *FsTestSuite) TestStat() {
	assert := suite.assert
	stat := suite.fs.StatFs("")
	assert.NotNil(stat)
}

func (suite *FsTestSuite) TestString() {
	assert := suite.assert
	assert.Equal(suite.fs.String(), "keywhiz-fs")
}
