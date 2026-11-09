# Which guard patterns remove handled errors

Type: grilling
Status: open
Blocked by: 04
Map: [errtrace v1 map](../map.md)

## Question

Given what "How error flows look in SSA" found to be recoverable, exactly which guard patterns (`==`, `errors.Is`, `errors.As`, type switches; returning nil vs replacing vs wrapping) does v1 recognise as removing an error from the Error Set, and what does it do with patterns it can't decide (keep the error, or mark it as possibly handled)?
