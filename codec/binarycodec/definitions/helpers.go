package definitions

// TypeCode returns the type code associated with the given type name.
func (d *Definitions) TypeCode(n string) (int32, error) {
	typeCode, ok := d.types[n]

	if !ok {
		return 0, &NotFoundError{
			Instance: "TypeName",
			Input:    n,
		}
	}
	return typeCode, nil
}

// FieldHeaderByName returns the field header struct associated with the given field name.
func (d *Definitions) FieldHeaderByName(n string) (*FieldHeader, error) {
	fi, ok := d.fields[n]

	if !ok {
		return nil, &NotFoundError{
			Instance: "FieldName",
			Input:    n,
		}
	}

	return fi.FieldHeader, nil
}

// FieldNameByHeader returns the field name associated with the given field header struct.
func (d *Definitions) FieldNameByHeader(fh FieldHeader) (string, error) {
	fim, ok := d.fieldIDNameMap[fh]

	if !ok {
		return "", &NotFoundErrorFieldHeader{
			Instance: "FieldHeader",
			Input:    fh,
		}
	}
	return fim, nil
}

// FieldInstanceByName returns the field instance struct associated with the given field name.
func (d *Definitions) FieldInstanceByName(n string) (*FieldInstance, error) {
	fi, ok := d.fields[n]

	if !ok {
		return nil, &NotFoundError{
			Instance: "FieldName",
			Input:    n,
		}
	}
	return fi, nil
}

// TransactionTypeCode returns the transaction type code associated with the transaction type name.
func (d *Definitions) TransactionTypeCode(n string) (int32, error) {
	txTypeCode, ok := d.transactionTypes[n]

	if !ok {
		return 0, &NotFoundError{
			Instance: "TransactionTypeName",
			Input:    n,
		}
	}
	return txTypeCode, nil
}

// TransactionTypeName returns the transaction type name associated with the transaction type code.
func (d *Definitions) TransactionTypeName(c int32) (string, error) {
	if name, ok := d.transactionTypeNames[c]; ok {
		return name, nil
	}
	return "", &NotFoundErrorInt{
		Instance: "TransactionTypeCode",
		Input:    c,
	}
}

// TransactionResultName returns the transaction result name associated with the transaction result type code.
func (d *Definitions) TransactionResultName(c int32) (string, error) {
	if name, ok := d.transactionResultNames[c]; ok {
		return name, nil
	}
	return "", &NotFoundErrorInt{
		Instance: "TransactionResultTypeCode",
		Input:    c,
	}
}

// TransactionResultCode returns the transaction result type code associated with the transaction result name.
func (d *Definitions) TransactionResultCode(n string) (int32, error) {
	txResultTypeCode, ok := d.transactionResults[n]

	if !ok {
		return 0, &NotFoundError{
			Instance: "TransactionResultName",
			Input:    n,
		}
	}
	return txResultTypeCode, nil
}

// LedgerEntryTypeCode returns the ledger entry type code associated with the ledger entry type name.
func (d *Definitions) LedgerEntryTypeCode(n string) (int32, error) {
	ledgerEntryTypeCode, ok := d.ledgerEntryTypes[n]

	if !ok {
		return 0, &NotFoundError{
			Instance: "LedgerEntryTypeName",
			Input:    n,
		}
	}
	return ledgerEntryTypeCode, nil
}

// LedgerEntryTypeName returns the ledger entry type name associated with the ledger entry type code.
func (d *Definitions) LedgerEntryTypeName(c int32) (string, error) {
	if name, ok := d.ledgerEntryTypeNames[c]; ok {
		return name, nil
	}
	return "", &NotFoundErrorInt{
		Instance: "LedgerEntryTypeCode",
		Input:    c,
	}
}

// DelegatablePermissionValue returns the delegatable permission value associated with the permission name.
func (d *Definitions) DelegatablePermissionValue(n string) (int32, error) {
	permissionValue, ok := d.delegatablePermissions[n]

	if !ok {
		return 0, &NotFoundError{
			Instance: "DelegatablePermissionName",
			Input:    n,
		}
	}
	return permissionValue, nil
}

// DelegatablePermissionName returns the delegatable permission name associated with the permission value.
func (d *Definitions) DelegatablePermissionName(v int32) (string, error) {
	if name, ok := d.delegatablePermissionNames[v]; ok {
		return name, nil
	}
	return "", &NotFoundErrorInt{
		Instance: "DelegatablePermissionValue",
		Input:    v,
	}
}
