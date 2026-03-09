# Binary Codec

This package contains functions to encode/decode to/from the [ripple binary serialization format](https://xrpl.org/serialization.html).

## API

### Encode

```go
encoded, err := binarycodec.Encode(jsonObject)
```

### Decode

```go
json, err := binarycodec.Decode(hexEncodedString)
```
### EncodeForMultisigning

```go
encoded, err := binarycodec.EncodeForMultisigning(jsonObject, xrpAccountID)
```

### EncodeForSigning

```go
encoded, err := binarycodec.EncodeForSigning(jsonObject)
```

### EncodeForSigningClaim

```go
encoded, err := binarycodec.EncodeForSigningClaim(jsonObject)
```

### EncodeQuality

```go
encoded, err := binarycodec.EncodeQuality(amountString)
```

### DecodeQuality

```go
decoded, err := binarycodec.DecodeQuality(encoded)
```

### DecodeLedgerData

```go
ledgerData, err := binarycodec.DecodeLedgerData(hexEncodedString)
```
