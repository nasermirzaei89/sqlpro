package sqlpro

import (
	"fmt"
	"reflect"
	"strings"
)

// checkData checks that the given data is either one of:
//
// *[]*strcut
// *[]struct
// []*struct
// []struct
// *struct
//
// For structs the function returns true, nil, for slices false, nil

func checkData(data interface{}) (reflect.Value, bool, error) {
	var (
		rv         reflect.Value
		structMode bool
	)

	err := func() (reflect.Value, bool, error) {
		return rv, false, fmt.Errorf("Insert/Update needs a slice of structs.")
	}

	rv = reflect.Indirect(reflect.ValueOf(data))

	switch rv.Type().Kind() {
	case reflect.Slice:
		switch rv.Type().Elem().Kind() {
		case reflect.Ptr:
			if rv.Type().Elem().Elem().Kind() != reflect.Struct {
				return err()
			}
		case reflect.Struct:
		default:
			return rv, false, fmt.Errorf("Insert/Update needs a slice of structs.")
		}
	case reflect.Struct:
		if !rv.CanAddr() {
			return err()
		}
		structMode = true
	default:
		return err()
	}

	return rv, structMode, nil
}

// Insert takes a table name and a struct and inserts
// the record in the DB.
// The given data needs to be:
//
// *[]*strcut
// *[]struct
// []*struct
// []struct
// struct
// *struct
//
// sqlpro will executes one INSERT statement per row.
// result.LastInsertId will be used to set the first primary
// key column.

func (db *DB) Insert(table string, data interface{}) error {
	var (
		rv         reflect.Value
		structMode bool
		err        error
	)

	rv, structMode, err = checkData(data)
	if err != nil {
		return err
	}

	if !structMode {
		for i := 0; i < rv.Len(); i++ {
			row := reflect.Indirect(rv.Index(i))
			insert_id, structInfo, err := db.insertStruct(table, row.Interface())
			if err != nil {
				return err
			}
			pk := structInfo.onlyPrimaryKey()
			if pk != nil {
				row.FieldByName(pk.name).SetInt(insert_id)
			}
		}
	} else {
		insert_id, structInfo, err := db.insertStruct(table, rv.Interface())
		if err != nil {
			return err
		}
		pk := structInfo.onlyPrimaryKey()
		if pk != nil {
			rv.FieldByName(pk.name).SetInt(insert_id)
		}
	}

	// data
	return nil
}

// InsertBulk takes a table name and a slice of struct and inserts
// the record in the DB with one Exec.
// The given data needs to be:
//
// *[]*strcut
// *[]struct
// []*struct
// []struct
//
// sqlpro will executes one INSERT statement per call.

func (db *DB) InsertBulk(table string, data interface{}) error {
	var (
		rv         reflect.Value
		structMode bool
		err        error
	)

	rv, structMode, err = checkData(data)
	if err != nil {
		return err
	}

	if structMode {
		return fmt.Errorf("InsertBulk: Need Slice to insert bulk.")
	}

	key_map := make(map[string]*fieldInfo, 0)
	rows := make([]map[string]interface{}, 0)
	for i := 0; i < rv.Len(); i++ {
		row := reflect.Indirect(rv.Index(i)).Interface()
		values, structInfo := db.valuesFromStruct(row)
		rows = append(rows, values)

		for key := range values {
			key_map[key] = structInfo[key]
		}
	}

	insert := make([]string, 0)
	keys := make([]string, 0, len(key_map))

	insert = append(insert, "INSERT INTO ", db.Esc(table), "(")
	idx := 0
	for key := range key_map {
		if idx > 0 {
			insert = append(insert, ",")
		}
		insert = append(insert, db.Esc(key))
		keys = append(keys, key)
		idx++
	}
	insert = append(insert, ") VALUES ")

	for idx, row := range rows {
		if idx > 0 {
			insert = append(insert, ",")
		}
		insert = append(insert, "(")
		for idx2, key := range keys {
			if idx2 > 0 {
				insert = append(insert, ",")
			}
			value, _ := row[key]
			escV, _, err := db.escValue(value, key_map[key])
			if err != nil {
				return err
			}
			insert = append(insert, escV)
		}
		insert = append(insert, ")")
	}

	_, err = db.exec(int64(rv.Len()), strings.Join(insert, ""))
	if err != nil {
		return err
	}
	return nil
}

func (db *DB) insertStruct(table string, row interface{}) (int64, structInfo, error) {

	values, info := db.valuesFromStruct(row)
	sql, args, err := db.insertClauseFromValues(table, values, info)
	if err != nil {
		return 0, nil, err
	}

	insert_id, err := db.exec(1, sql, args...)
	if err != nil {
		return 0, nil, err
	}
	return insert_id, info, nil
}

func (db *DB) insertClauseFromValues(table string, values map[string]interface{}, info structInfo) (string, []interface{}, error) {
	cols := make([]string, 0, len(values))
	vs := make([]string, 0, len(values))
	args := make([]interface{}, 0, len(values))

	for col, value := range values {
		cols = append(cols, db.Esc(col))
		escV, driverV, err := db.escValue(value, info[col])
		if err != nil {
			panic(err)
		}
		if escV == "" {
			vs = append(vs, "?")
			args = append(args, driverV)
		} else {
			vs = append(vs, escV)
		}
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES(%s)",
		db.Esc(table),
		strings.Join(cols, ","),
		strings.Join(vs, ","),
	), args, nil
}

func (db *DB) updateClauseFromRow(table string, row interface{}) (string, []interface{}, error) {

	var (
		valid bool
		args  []interface{}
	)

	values, structInfo := db.valuesFromStruct(row)

	update := strings.Builder{}
	update.WriteString("UPDATE ")
	update.WriteString(db.Esc(table))
	update.WriteString(" SET ")

	where := strings.Builder{}
	where.WriteString(" WHERE ")

	idx := 0
	for key, value := range values {
		if structInfo.primaryKey(key) {
			// skip primary keys for update
			escV, driverV, err := db.escValue(value, structInfo[key])
			if err != nil {
				return "", args, err
			}
			if escV == "null" {
				return "", args, fmt.Errorf("Unable to build UPDATE clause with <nil> key: %s", key)
			}
			where.WriteString(db.Esc(key))
			where.WriteString("=")
			if escV == "" {
				where.WriteRune(db.PlaceholderValue)
				args = append(args, driverV)
			} else {
				where.WriteString(escV)
			}
			valid = true
		} else {
			if idx > 0 {
				update.WriteString(",")
			}
			update.WriteString(db.Esc(key))
			update.WriteString("=")
			escV, driverV, err := db.escValue(value, structInfo[key])
			if err != nil {
				return "", args, err
			}
			if escV == "" {
				update.WriteRune(db.PlaceholderValue)
				args = append(args, driverV)
			} else {
				update.WriteString(escV)
			}
			idx++
		}
	}

	if !valid {
		return "", args, fmt.Errorf("Unable to build UPDATE clause, at least one key needed.")
	}

	// Add where clause
	return update.String() + where.String(), args, nil
}

// Update updates the given struct or slice of structs
// The WHERE clause is put together from the "pk" columns.
// If not all "pk" columns have non empty values, Update returns
// an error.
func (db *DB) Update(table string, data interface{}) error {
	var (
		rv         reflect.Value
		structMode bool
		err        error
		update     string
		args       []interface{}
	)

	rv, structMode, err = checkData(data)
	if err != nil {
		return err
	}

	if structMode {
		update, args, err = db.updateClauseFromRow(table, rv.Interface())
		if err != nil {
			return err
		}
		_, err = db.exec(1, update, args...)
		if err != nil {
			return err
		}
	} else {
		for i := 0; i < rv.Len(); i++ {
			row := reflect.Indirect(rv.Index(i))
			update, args, err = db.updateClauseFromRow(table, row.Interface())
			if err != nil {
				return err
			}
			_, err = db.exec(1, update, args...)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Save saves the given data. It performs an INSERT if the only
// primary key is zero, and and UPDATE if it is not. It panics
// if it the record has no primary key or less than one
func (db *DB) Save(table string, data interface{}) error {

	rv, structMode, err := checkData(data)
	if err != nil {
		return err
	}

	if structMode {
		return db.saveRow(table, data)
	} else {
		for i := 0; i < rv.Len(); i++ {
			err = db.saveRow(table, rv.Index(i).Interface())
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (db *DB) saveRow(table string, data interface{}) error {
	row := reflect.Indirect(reflect.ValueOf(data))

	values, info := db.valuesFromStruct(row.Interface())
	pk := info.onlyPrimaryKey()

	if pk == nil {
		return fmt.Errorf("Save needs a struct with exactly one 'pk' field.")
	}

	pk_value, ok := values[pk.dbName]
	if !ok || isZero(pk_value) {
		return db.Insert(table, data)
	} else {
		return db.Update(table, data)
	}

}

// valuesFromStruct returns the relevant values
// from struct, as map
func (db *DB) valuesFromStruct(data interface{}) (map[string]interface{}, structInfo) {
	var (
		info   structInfo
		values map[string]interface{}
		dataV  reflect.Value
	)

	values = make(map[string]interface{}, 0)
	dataV = reflect.ValueOf(data)

	info = getStructInfo(dataV.Type())

	for _, fieldInfo := range info {
		dataF := dataV.FieldByName(fieldInfo.name)

		actualData := dataF.Interface()
		isZero := isZero(actualData)

		if isZero && fieldInfo.omitEmpty {
			continue
		}
		values[fieldInfo.dbName] = actualData
		// log.Printf("Name: %s Value: %v %v", fieldInfo.name, dataF.Interface(), isZero)
	}
	return values, info
}

// isZero returns true if given "x" equals Go's empty value.
func isZero(x interface{}) bool {
	return reflect.DeepEqual(x, reflect.Zero(reflect.TypeOf(x)).Interface())
}
