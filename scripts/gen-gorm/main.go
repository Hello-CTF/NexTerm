package main

import (
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"gorm.io/gen"
)

func main() {
	generator := gen.NewGenerator(gen.Config{
		OutPath:           "internal/store/query",
		ModelPkgPath:      "internal/store/model",
		FieldNullable:     true,
		FieldWithIndexTag: true,
		FieldWithTypeTag:  true,
	})
	generator.ApplyBasic(model.AllModels...)
	generator.Execute()
}
