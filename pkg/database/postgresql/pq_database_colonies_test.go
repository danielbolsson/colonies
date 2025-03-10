package postgresql

import (
	"testing"

	"github.com/colonyos/colonies/pkg/core"
	"github.com/colonyos/colonies/pkg/utils"
	"github.com/stretchr/testify/assert"
)

func TestAddColony(t *testing.T) {
	db, err := PrepareTests()
	assert.Nil(t, err)

	defer db.Close()

	colony := core.CreateColony(core.GenerateRandomID(), "test_colony_name")

	err = db.AddColony(colony)
	assert.Nil(t, err)

	colonies, err := db.GetColonies()
	assert.Nil(t, err)

	colonyFromDB := colonies[0]
	assert.True(t, colony.Equals(colonyFromDB))

	colonyFromDB, err = db.GetColonyByID(colony.ID)
	assert.Nil(t, err)
	assert.True(t, colony.Equals(colonyFromDB))
}

func TestAddTwoColonies(t *testing.T) {
	db, err := PrepareTests()
	assert.Nil(t, err)

	defer db.Close()

	colony1 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_1")
	err = db.AddColony(colony1)
	assert.Nil(t, err)

	colony2 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_2")
	err = db.AddColony(colony2)
	assert.Nil(t, err)

	var colonies []*core.Colony
	colonies = append(colonies, colony1)
	colonies = append(colonies, colony2)

	coloniesFromDB, err := db.GetColonies()
	assert.Nil(t, err)
	assert.True(t, core.IsColonyArraysEqual(colonies, coloniesFromDB))
}

func TestGetColonyByID(t *testing.T) {
	db, err := PrepareTests()
	assert.Nil(t, err)

	defer db.Close()

	colony1 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_1")

	err = db.AddColony(colony1)
	assert.Nil(t, err)

	colony2 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_2")

	err = db.AddColony(colony2)
	assert.Nil(t, err)

	colonyFromDB, err := db.GetColonyByID(colony1.ID)
	assert.Nil(t, err)
	assert.Equal(t, colony1.ID, colonyFromDB.ID)

	colonyFromDB, err = db.GetColonyByID(core.GenerateRandomID())
	assert.Nil(t, err)
}

func TestDeleteColonies(t *testing.T) {
	db, err := PrepareTests()
	assert.Nil(t, err)

	defer db.Close()

	colony1 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_1")

	err = db.AddColony(colony1)
	assert.Nil(t, err)

	colony2 := core.CreateColony(core.GenerateRandomID(), "test_colony_name_2")

	err = db.AddColony(colony2)
	assert.Nil(t, err)

	generator1 := utils.FakeGenerator(t, colony1.ID)
	generator1.ID = core.GenerateRandomID()
	err = db.AddGenerator(generator1)
	assert.Nil(t, err)

	generator2 := utils.FakeGenerator(t, colony2.ID)
	generator2.ID = core.GenerateRandomID()
	err = db.AddGenerator(generator2)
	assert.Nil(t, err)

	cron1 := utils.FakeCron(t, colony1.ID)
	cron1.ID = core.GenerateRandomID()
	err = db.AddCron(cron1)
	assert.Nil(t, err)

	cron2 := utils.FakeCron(t, colony2.ID)
	cron2.ID = core.GenerateRandomID()
	err = db.AddCron(cron2)
	assert.Nil(t, err)

	runtime1 := utils.CreateTestRuntime(colony1.ID)
	err = db.AddRuntime(runtime1)
	assert.Nil(t, err)

	runtime2 := utils.CreateTestRuntime(colony1.ID)
	err = db.AddRuntime(runtime2)
	assert.Nil(t, err)

	runtime3 := utils.CreateTestRuntime(colony2.ID)
	err = db.AddRuntime(runtime3)
	assert.Nil(t, err)

	err = db.DeleteColonyByID(colony1.ID)
	assert.Nil(t, err)

	colonyFromDB, err := db.GetColonyByID(colony1.ID)
	assert.Nil(t, err)
	assert.Nil(t, colonyFromDB)

	runtimeFromDB, err := db.GetRuntimeByID(runtime1.ID)
	assert.Nil(t, err)
	assert.Nil(t, runtimeFromDB)

	runtimeFromDB, err = db.GetRuntimeByID(runtime2.ID)
	assert.Nil(t, err)
	assert.Nil(t, runtimeFromDB)

	runtimeFromDB, err = db.GetRuntimeByID(runtime3.ID)
	assert.Nil(t, err)
	assert.NotNil(t, runtimeFromDB) // Belongs to Colony 2 and should therefore NOT be deleted

	generatorFromDB, err := db.GetGeneratorByID(generator1.ID)
	assert.Nil(t, err)
	assert.Nil(t, generatorFromDB) // Should have been deleted

	generatorFromDB, err = db.GetGeneratorByID(generator2.ID)
	assert.Nil(t, err)
	assert.NotNil(t, generatorFromDB) // Should NOT have been deleted

	cronFromDB, err := db.GetCronByID(cron1.ID)
	assert.Nil(t, err)
	assert.Nil(t, cronFromDB) // Should have been deleted

	cronFromDB, err = db.GetCronByID(cron2.ID)
	assert.Nil(t, err)
	assert.NotNil(t, cronFromDB) // Should NOT have been deleted
}

func TestCountColonies(t *testing.T) {
	db, err := PrepareTests()
	assert.Nil(t, err)

	defer db.Close()

	coloniesCount, err := db.CountColonies()
	assert.Nil(t, err)
	assert.True(t, coloniesCount == 0)

	colony := core.CreateColony(core.GenerateRandomID(), "test_colony_name")
	err = db.AddColony(colony)
	assert.Nil(t, err)

	coloniesCount, err = db.CountColonies()
	assert.Nil(t, err)
	assert.True(t, coloniesCount == 1)

	colony = core.CreateColony(core.GenerateRandomID(), "test_colony_name2")
	err = db.AddColony(colony)
	assert.Nil(t, err)

	coloniesCount, err = db.CountColonies()
	assert.Nil(t, err)
	assert.True(t, coloniesCount == 2)
}
