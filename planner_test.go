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

func TestServer(t *testing.T) {
	server := testutil.NewTestServer(t, nil)
	defer server.Close()

	contracts := map[string]*Contract{}
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

		contracts[ar.Name] = NewContract(receipt.ContractAddress, ar.ABI)
	}

	vm := contracts["TestableVM"]
	events := contracts["Events"]
	math := contracts["Math"]

	p := NewPlanner()
	ret1 := p.Add(math.Call("add", 1, 2))
	ret2 := p.Add(math.Call("add", 3, 4))
	ret3 := p.Add(math.Call("add", ret1, ret2))
	p.Add(events.Call("logUint", ret3))

	planInput, err := p.Plan()
	assert.NoError(t, err)

	input, err := vm.abi.GetMethod("execute").Encode([]interface{}{planInput.Commands, planInput.State})
	assert.NoError(t, err)

	receipt, err := server.SendTxn(&ethgo.Transaction{
		To:    &vm.addr,
		Input: input,
	})
	assert.NoError(t, err)

	res, err := abi.ParseLog(events.abi.Events["LogUint"].Inputs, receipt.Logs[0])
	assert.NoError(t, err)
	assert.Equal(t, res["message"].(*big.Int), big.NewInt(10))
}
