package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/jinzhu/gorm"
	"github.com/jinzhu/gorm/dialects/common/sqlbuilder"
	"github.com/jinzhu/gorm/model"
	"github.com/jinzhu/gorm/schema"
)

// Dialect Sqlite3 Dialect for GORM
type Dialect struct {
	DB *sql.DB
}

// Quote quote for value
func (dialect Dialect) Quote(name string) string {
	return fmt.Sprintf(`"%s"`, name)
}

// Insert insert
func (dialect *Dialect) Insert(tx *gorm.DB) (err error) {
	var (
		args            []interface{}
		assignmentsChan = sqlbuilder.GetAssignmentFields(tx)
		tableNameChan   = sqlbuilder.GetTable(tx)
		primaryFields   []*model.Field
	)

	s := bytes.NewBufferString("INSERT INTO ")
	s.WriteString(dialect.Quote(<-tableNameChan))

	if assignments := <-assignmentsChan; len(assignments) > 0 {
		columns := []string{}

		// Write columns (column1, column2, column3)
		s.WriteString(" (")

		// Write values (v1, v2, v3), (v2-1, v2-2, v2-3)
		valueBuffer := bytes.NewBufferString("VALUES ")

		for idx, fields := range assignments {
			var primaryField *model.Field
			if idx != 0 {
				valueBuffer.WriteString(",")
			}
			valueBuffer.WriteString(" (")

			for j, field := range fields {
				if field.Field.IsPrimaryKey && primaryField == nil || field.Field.DBName == "id" {
					primaryField = field
				}

				if idx == 0 {
					columns = append(columns, field.Field.DBName)
					if j != 0 {
						s.WriteString(", ")
					}
					s.WriteString(dialect.Quote(field.Field.DBName))
				}

				if j != 0 {
					valueBuffer.WriteString(", ")
				}
				valueBuffer.WriteString("?")

				if (field.Field.IsPrimaryKey || field.HasDefaultValue) && field.IsBlank {
					args = append(args, nil)
				} else {
					args = append(args, field.Value.Interface())
				}
			}

			primaryFields = append(primaryFields, primaryField)
			valueBuffer.WriteString(")")
		}
		s.WriteString(") ")

		_, err = valueBuffer.WriteTo(s)
	} else {
		s.WriteString(" DEFAULT VALUES")
	}

	result, err := dialect.DB.Exec(s.String(), args...)

	if err == nil {
		var lastInsertID int64
		tx.RowsAffected, _ = result.RowsAffected()
		lastInsertID, err = result.LastInsertId()
		if len(primaryFields) == int(tx.RowsAffected) {
			startID := lastInsertID - tx.RowsAffected + 1
			for i, primaryField := range primaryFields {
				tx.AddError(primaryField.Set(startID + int64(i)))
			}
		}
	}
	return
}

// Query query
func (dialect *Dialect) Query(tx *gorm.DB) (err error) {
	var (
		args           []interface{}
		tableNameChan  = sqlbuilder.GetTable(tx)
		joinChan       = sqlbuilder.BuildJoinCondition(tx)
		conditionsChan = sqlbuilder.BuildConditions(tx)
		groupChan      = sqlbuilder.BuildGroupCondition(tx)
		orderChan      = sqlbuilder.BuildOrderCondition(tx)
		limitChan      = sqlbuilder.BuildLimitCondition(tx)
	)

	s := bytes.NewBufferString("SELECT ")

	// FIXME quote, add table
	columns := tx.Statement.Select.Columns
	if len(columns) > 0 {
		args = append(args, tx.Statement.Select.Args...)
	} else {
		columns = []string{"*"}
	}

	for idx, column := range columns {
		if idx != 0 {
			s.WriteString(",")
		}
		s.WriteString(column)
	}

	s.WriteString(" FROM ")
	s.WriteString(dialect.Quote(<-tableNameChan))

	// Join SQL
	if builder := <-joinChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if len(tx.Statement.Conditions) > 0 {
		builder := <-conditionsChan
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-groupChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-orderChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-limitChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	rows, err := dialect.DB.Query(s.String(), args...)

	if err == nil {
		err = scanRows(rows, tx.Statement.Dest)
	}

	return
}

func scanRows(rows *sql.Rows, values interface{}) (err error) {
	var (
		isSlice bool
		results = indirect(reflect.ValueOf(values))
	)
	columns, err := rows.Columns()

	if kind := results.Kind(); kind == reflect.Slice {
		isSlice = true
		results.Set(reflect.MakeSlice(results.Type().Elem(), 0, 0))
	}

	for rows.Next() {
		elem := results
		if isSlice {
			elem = reflect.New(results.Type().Elem()).Elem()
		}

		dests, err := toScanMap(columns, elem)

		if err == nil {
			err = rows.Scan(dests...)
		}

		if err != nil {
			return err
		}

		if isSlice {
			results.Set(reflect.Append(results, elem))
		}
	}

	return
}

func toScanMap(columns []string, elem reflect.Value) (results []interface{}, err error) {
	var ignored interface{}
	results = make([]interface{}, len(columns))

	switch elem.Kind() {
	case reflect.Map:
		for idx, column := range columns {
			var value interface{}
			elem.SetMapIndex(reflect.ValueOf(column), reflect.ValueOf(value))
			results[idx] = &value
		}
	case reflect.Struct:
		fieldsMap := model.Parse(elem.Addr().Interface()).FieldsMap()
		for idx, column := range columns {
			if f, ok := fieldsMap[column]; ok {
				results[idx] = f.Value.Interface()
			} else {
				results[idx] = &ignored
			}
		}
	case reflect.Ptr:
		if elem.IsNil() {
			elem.Set(reflect.New(elem.Type().Elem()))
		}
		return toScanMap(columns, elem)
	default:
		return nil, errors.New("unsupported destination")
	}
	return
}

func indirect(reflectValue reflect.Value) reflect.Value {
	for reflectValue.Kind() == reflect.Ptr {
		reflectValue = reflectValue.Elem()
	}
	return reflectValue
}

// Update update
func (dialect *Dialect) Update(tx *gorm.DB) (err error) {
	var (
		args            []interface{}
		tableNameChan   = sqlbuilder.GetTable(tx)
		conditionsChan  = sqlbuilder.BuildConditions(tx)
		assignmentsChan = sqlbuilder.GetAssignmentFields(tx)
		orderChan       = sqlbuilder.BuildOrderCondition(tx)
		limitChan       = sqlbuilder.BuildLimitCondition(tx)
	)

	s := bytes.NewBufferString("UPDATE ")
	s.WriteString(dialect.Quote(<-tableNameChan))
	s.WriteString(" SET ")
	if assignments := <-assignmentsChan; len(assignments) > 0 {
		for _, fields := range assignments {
			for _, field := range fields {
				s.WriteString(dialect.Quote(field.Field.DBName))
				s.WriteString(" = ?")
				args = append(args, field.Value.Interface())
			}
			// TODO update with multiple records
		}
	}

	if len(tx.Statement.Conditions) > 0 {
		builder := <-conditionsChan
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-orderChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-limitChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	_, err = dialect.DB.Exec(s.String(), args...)
	return err
}

// Delete delete
func (dialect *Dialect) Delete(tx *gorm.DB) (err error) {
	var (
		args           []interface{}
		tableNameChan  = sqlbuilder.GetTable(tx)
		conditionsChan = sqlbuilder.BuildConditions(tx)
		orderChan      = sqlbuilder.BuildOrderCondition(tx)
		limitChan      = sqlbuilder.BuildLimitCondition(tx)
	)
	s := bytes.NewBufferString("DELETE FROM ")
	s.WriteString(dialect.Quote(<-tableNameChan))

	if len(tx.Statement.Conditions) > 0 {
		builder := <-conditionsChan
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-orderChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	if builder := <-limitChan; builder != nil {
		_, err = builder.SQL.WriteTo(s)
		args = append(args, builder.Args...)
	}

	_, err = dialect.DB.Exec(s.String(), args...)
	return
}

// AutoMigrate auto migrate database
func (dialect *Dialect) AutoMigrate(value interface{}) (err error) {
	// create table

	// create missed column

	// safe upgrade some fields (like size, change data type)

	// create missed foreign key

	// create missed index
	return nil
}

func (dialect *Dialect) HasTable(name string) bool {
	return false
}

func (dialect *Dialect) CreateTable(value interface{}) error {
	s := schema.Parse(value)
	return nil
}
