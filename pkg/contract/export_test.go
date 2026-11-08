package contract

// The answer key and the schema for TestEngineAnswerKey, which decodes
// through pkg/nitrovin and so lives in package contract_test.
type KeyedVIN = keyedVIN

var (
	LoadAnswerKey = loadAnswerKey
	SchemaDef     = schemaDef
	ValidateJSON  = validate
)
