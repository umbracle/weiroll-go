package weirollgo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/umbracle/ethgo"
	"github.com/umbracle/ethgo/abi"
	"github.com/umbracle/ethgo/testutil"
)

type artifact struct {
	Name        string   `json:"contractName"`
	ABI         *abi.ABI `json:"abi"`
	Bytecode    []byte
	BytecodeRaw string `json:"bytecode"`
}

func readArtifact(path string) (a *artifact) {
	fullPath := filepath.Join("artifacts", path+".sol/"+filepath.Base(path)+".json")

	data, err := os.ReadFile(fullPath)
	if err != nil {
		panic(fmt.Sprintf("BUG: failed to read file %s: %v", fullPath, err))
	}
	if err := json.Unmarshal(data, &a); err != nil {
		panic(fmt.Sprintf("BUG: failed to decode artifact %s: %v", fullPath, err))
	}

	bytecode, err := hex.DecodeString(a.BytecodeRaw[2:])
	if err != nil {
		panic(fmt.Sprintf("BUG: failed to decode bytecode %s: %v", fullPath, err))
	}
	a.Bytecode = bytecode
	return
}

type contract struct {
	addr ethgo.Address
	*artifact
}

func TestServer(t *testing.T) {
	server := testutil.NewTestServer(t, nil)
	defer server.Close()

	contracts := map[string]*contract{}
	for _, name := range []string{
		"test/TestableVM",
		"test/Sender",
		"Libraries/Events",
		"Libraries/Math",
	} {
		ar := readArtifact(name)

		receipt, err := server.SendTxn(&ethgo.Transaction{
			Input: ar.Bytecode,
		})
		assert.NoError(t, err)

		contracts[ar.Name] = &contract{
			artifact: ar,
			addr:     receipt.ContractAddress,
		}
	}

	vm := contracts["TestableVM"]
	events := contracts["Events"]
	math := contracts["Math"]

	p := NewPlanner()
	ret1 := p.Add(&Command{
		Address: math.addr,
		Method:  math.ABI.GetMethod("add"),
		Args:    []interface{}{1, 2},
	})
	ret2 := p.Add(&Command{
		Address: math.addr,
		Method:  math.ABI.GetMethod("add"),
		Args:    []interface{}{3, 4},
	})
	ret3 := p.Add(&Command{
		Address: math.addr,
		Method:  math.ABI.GetMethod("add"),
		Args:    []interface{}{ret1, ret2},
	})
	p.Add(&Command{
		Address: events.addr,
		Method:  events.ABI.GetMethod("logUint"),
		Args:    []interface{}{ret3},
	})

	planInput, err := p.Plan()
	assert.NoError(t, err)

	input, err := vm.ABI.GetMethod("execute").Encode([]interface{}{planInput.Commands, planInput.State})
	assert.NoError(t, err)

	receipt, err := server.SendTxn(&ethgo.Transaction{
		To:    &vm.addr,
		Input: input,
	})
	assert.NoError(t, err)

	res, err := abi.ParseLog(events.ABI.Events["LogUint"].Inputs, receipt.Logs[0])
	assert.NoError(t, err)
	assert.Equal(t, res["message"].(*big.Int), big.NewInt(10))
}
