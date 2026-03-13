package nft_test

// NFTokenDir_test.go - NFT directory (page management) tests
// Reference: rippled/src/test/app/NFTokenDir_test.cpp

import (
	"sort"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/nft"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/nftoken"
)

// Seeds that produce AccountIDs with identical low 32-bits (0x9a8ebed3)
// Used for testing page overflow scenarios with 33 equivalent NFTs
var seedsLow32_9a8ebed3 = []string{
	"sp6JS7f14BuwFY8Mw5FnqmbciPvH6", //  0. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw5MBGbyMSsXLp", //  1. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw5S4PnDyBdKKm", //  2. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw6kcXpM2enE35", //  3. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw6tuuSMMwyJ44", //  4. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8E8JWLQ1P8pt", //  5. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8WwdgWkCHhEx", //  6. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8XDUYvU6oGhQ", //  7. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8ceVGL4M1zLQ", //  8. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8fdSwLCZWDFd", //  9. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mw8zuF6Fg65i1E", // 10. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwF2k7bihVfqes", // 11. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwF6X24WXGn557", // 12. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwFMpn7strjekg", // 13. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwFSdy9sYVrwJs", // 14. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwFdMcLy9UkrXn", // 15. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwFdbwFm1AAboa", // 16. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwFdr5AhKThVtU", // 17. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwjFc3Q9YatvAw", // 18. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwjRXcNs1ozEXn", // 19. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwkQGUKL7v1FBt", // 20. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mwkamsoxx1wECt", // 21. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mwm3hus1dG6U8y", // 22. 0x9a8ebed3
	"sp6JS7f14BuwFY8Mwm589M8vMRpXF", // 23. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwmJTRJ4Fqz1A3", // 24. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwmRfy8fer4QbL", // 25. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwmkkFx1HtgWRx", // 26. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwmwP9JFdKa4PS", // 27. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwoXWJLB3ciHfo", // 28. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwoYc1gTtT2mWL", // 29. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwogXtHH7FNVoo", // 30. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwoqYoA9P8gf3r", // 31. 0x9a8ebed3
	"sp6JS7f14BuwFY8MwoujwMJofGnsA", // 32. 0x9a8ebed3
}

// Seeds for consecutive packing test (low 32-bits = 0x115d0525)
var seedsLow32_115d0525 = []string{
	"sp6JS7f14BuwFY8Mw56vZeiBuhePx", //  0. 0x115d0525
	"sp6JS7f14BuwFY8Mw5BodF9tGuTUe", //  1. 0x115d0525
	"sp6JS7f14BuwFY8Mw5EnhC1cg84J7", //  2. 0x115d0525
	"sp6JS7f14BuwFY8Mw5P913Cunr2BK", //  3. 0x115d0525
	"sp6JS7f14BuwFY8Mw5Pru7eLo1XzT", //  4. 0x115d0525
	"sp6JS7f14BuwFY8Mw61SLUC8UX2m8", //  5. 0x115d0525
	"sp6JS7f14BuwFY8Mw6AsBF9TpeMpq", //  6. 0x115d0525
	"sp6JS7f14BuwFY8Mw84XqrBZkU2vE", //  7. 0x115d0525
	"sp6JS7f14BuwFY8Mw89oSU6dBk3KB", //  8. 0x115d0525
	"sp6JS7f14BuwFY8Mw89qUKCyDmyzj", //  9. 0x115d0525
	"sp6JS7f14BuwFY8Mw8GfqQ9VRZ8tm", // 10. 0x115d0525
	"sp6JS7f14BuwFY8Mw8LtW3VqrqMks", // 11. 0x115d0525
	"sp6JS7f14BuwFY8Mw8ZrAkJc2sHew", // 12. 0x115d0525
	"sp6JS7f14BuwFY8Mw8jpkYSNrD3ah", // 13. 0x115d0525
	"sp6JS7f14BuwFY8MwF2mshd786m3V", // 14. 0x115d0525
	"sp6JS7f14BuwFY8MwFHfXq9x5NbPY", // 15. 0x115d0525
	"sp6JS7f14BuwFY8MwFrjWq5LAB8NT", // 16. 0x115d0525
	"sp6JS7f14BuwFY8Mwj4asgSh6hQZd", // 17. 0x115d0525
	"sp6JS7f14BuwFY8Mwj7ipFfqBSRrE", // 18. 0x115d0525
	"sp6JS7f14BuwFY8MwjHqtcvGav8uW", // 19. 0x115d0525
	"sp6JS7f14BuwFY8MwjLp4sk5fmzki", // 20. 0x115d0525
	"sp6JS7f14BuwFY8MwjioHuYb3Ytkx", // 21. 0x115d0525
	"sp6JS7f14BuwFY8MwkRjHPXWi7fGN", // 22. 0x115d0525
	"sp6JS7f14BuwFY8MwkdVdPV3LjNN1", // 23. 0x115d0525
	"sp6JS7f14BuwFY8MwkxUtVY5AXZFk", // 24. 0x115d0525
	"sp6JS7f14BuwFY8Mwm4jQzdfTbY9F", // 25. 0x115d0525
	"sp6JS7f14BuwFY8MwmCucYAqNp4iF", // 26. 0x115d0525
	"sp6JS7f14BuwFY8Mwo2bgdFtxBzpF", // 27. 0x115d0525
	"sp6JS7f14BuwFY8MwoGwD7v4U6qBh", // 28. 0x115d0525
	"sp6JS7f14BuwFY8MwoUczqFADMoXi", // 29. 0x115d0525
	"sp6JS7f14BuwFY8MwoY1xZeGd3gAr", // 30. 0x115d0525
	"sp6JS7f14BuwFY8MwomVCbfkv4kYZ", // 31. 0x115d0525
	"sp6JS7f14BuwFY8MwoqbrPSr4z13F", // 32. 0x115d0525
}

// Seeds for lopsided split test - split and add to high page
// Contains groups with identical low 32-bits: 0x1d2932ea, 0x208dbc24, 0x309b67ed, 0x40d4b96f, 0x503b6ba9
var seedsSplitAndAddToHi = []string{
	"sp6JS7f14BuwFY8Mw5p3b8jjQBBTK", //  0. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6F7X3EiGKazu", //  1. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6FxjntJJfKXq", //  2. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6eSF1ydEozJg", //  3. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6koPB91um2ej", //  4. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6m6D64iwquSe", //  5. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw5rC43sN4adC2", //  6. 0x208dbc24
	"sp6JS7f14BuwFY8Mw65L9DDQqgebz", //  7. 0x208dbc24
	"sp6JS7f14BuwFY8Mw65nKvU8pPQNn", //  8. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6bxZLyTrdipw", //  9. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6d5abucntSoX", // 10. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6qXK5awrRRP8", // 11. 0x208dbc24
	"sp6JS7f14BuwFY8Mw66EBtMxoMcCa", // 12. 0x309b67ed
	"sp6JS7f14BuwFY8Mw66dGfE9jVfGv", // 13. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6APdZa7PH566", // 14. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6C3QX5CZyET5", // 15. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6CSysFf8GvaR", // 16. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6c7QSDmoAeRV", // 17. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6mvonveaZhW7", // 18. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6vtHHG7dYcXi", // 19. 0x309b67ed
	"sp6JS7f14BuwFY8Mw66yppUNxESaw", // 20. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6ATYQvobXiDT", // 21. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6bis8D1Wa9Uy", // 22. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6cTiGCWA8Wfa", // 23. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6eAy2fpXmyYf", // 24. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6icn58TRs8YG", // 25. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw68tj2eQEWoJt", // 26. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6AjnAinNnMHT", // 27. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6CKDUwB4LrhL", // 28. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6d2yPszEFA6J", // 29. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6jcBQBH3PfnB", // 30. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6qxx19KSnN1w", // 31. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6ut1hFrqWoY5", // 32. 0x503b6ba9 (split: added to upper page)
}

// Seeds for lopsided split test - split and add to low page
var seedsSplitAndAddToLo = []string{
	"sp6JS7f14BuwFY8Mw5p3b8jjQBBTK", //  0. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6F7X3EiGKazu", //  1. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6FxjntJJfKXq", //  2. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6eSF1ydEozJg", //  3. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6koPB91um2ej", //  4. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw6m6D64iwquSe", //  5. 0x1d2932ea
	"sp6JS7f14BuwFY8Mw5rC43sN4adC2", //  6. 0x208dbc24
	"sp6JS7f14BuwFY8Mw65L9DDQqgebz", //  7. 0x208dbc24
	"sp6JS7f14BuwFY8Mw65nKvU8pPQNn", //  8. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6bxZLyTrdipw", //  9. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6d5abucntSoX", // 10. 0x208dbc24
	"sp6JS7f14BuwFY8Mw6qXK5awrRRP8", // 11. 0x208dbc24
	"sp6JS7f14BuwFY8Mw66EBtMxoMcCa", // 12. 0x309b67ed
	"sp6JS7f14BuwFY8Mw66dGfE9jVfGv", // 13. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6APdZa7PH566", // 14. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6C3QX5CZyET5", // 15. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6CSysFf8GvaR", // 16. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6c7QSDmoAeRV", // 17. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6mvonveaZhW7", // 18. 0x309b67ed
	"sp6JS7f14BuwFY8Mw6vtHHG7dYcXi", // 19. 0x309b67ed
	"sp6JS7f14BuwFY8Mw66yppUNxESaw", // 20. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6ATYQvobXiDT", // 21. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6bis8D1Wa9Uy", // 22. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6cTiGCWA8Wfa", // 23. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6eAy2fpXmyYf", // 24. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw6icn58TRs8YG", // 25. 0x40d4b96f
	"sp6JS7f14BuwFY8Mw68tj2eQEWoJt", // 26. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6AjnAinNnMHT", // 27. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6CKDUwB4LrhL", // 28. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6d2yPszEFA6J", // 29. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6jcBQBH3PfnB", // 30. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6qxx19KSnN1w", // 31. 0x503b6ba9
	"sp6JS7f14BuwFY8Mw6xCigaMwC6Dp", // 32. 0x309b67ed (split: added to lower page)
}

// Seeds for fixNFTokenDirV1 test - 17 in high group
var seedsSeventeenHi = []string{
	"sp6JS7f14BuwFY8Mw5EYu5z86hKDL", //  0. 0x399187e9
	"sp6JS7f14BuwFY8Mw5PUAMwc5ygd7", //  1. 0x399187e9
	"sp6JS7f14BuwFY8Mw5R3xUBcLSeTs", //  2. 0x399187e9
	"sp6JS7f14BuwFY8Mw5W6oS5sdC3oF", //  3. 0x399187e9
	"sp6JS7f14BuwFY8Mw5pYc3D9iuLcw", //  4. 0x399187e9
	"sp6JS7f14BuwFY8Mw5pfGVnhcdp3b", //  5. 0x399187e9
	"sp6JS7f14BuwFY8Mw6jS6RdEqXqrN", //  6. 0x399187e9
	"sp6JS7f14BuwFY8Mw6krt6AKbvRXW", //  7. 0x399187e9
	"sp6JS7f14BuwFY8Mw6mnVBQq7cAN2", //  8. 0x399187e9
	"sp6JS7f14BuwFY8Mw8ECJxPjmkufQ", //  9. 0x399187e9
	"sp6JS7f14BuwFY8Mw8asgzcceGWYm", // 10. 0x399187e9
	"sp6JS7f14BuwFY8MwF6J3FXnPCgL8", // 11. 0x399187e9
	"sp6JS7f14BuwFY8MwFEud2w5czv5q", // 12. 0x399187e9
	"sp6JS7f14BuwFY8MwFNxKVqJnx8P5", // 13. 0x399187e9
	"sp6JS7f14BuwFY8MwFnTCXg3eRidL", // 14. 0x399187e9
	"sp6JS7f14BuwFY8Mwj47hv1vrDge6", // 15. 0x399187e9
	"sp6JS7f14BuwFY8MwjJCwYr9zSfAv", // 16. 0xabb11898
	"sp6JS7f14BuwFY8MwjYa5yLkgCLuT", // 17. 0xabb11898
	"sp6JS7f14BuwFY8MwjenxuJ3TH2Bc", // 18. 0xabb11898
	"sp6JS7f14BuwFY8MwjriN7Ui11NzB", // 19. 0xabb11898
	"sp6JS7f14BuwFY8Mwk3AuoJNSEo34", // 20. 0xabb11898
	"sp6JS7f14BuwFY8MwkT36hnRv8hTo", // 21. 0xabb11898
	"sp6JS7f14BuwFY8MwkTQixEXfi1Cr", // 22. 0xabb11898
	"sp6JS7f14BuwFY8MwkYJaZM1yTJBF", // 23. 0xabb11898
	"sp6JS7f14BuwFY8Mwkc4k1uo85qp2", // 24. 0xabb11898
	"sp6JS7f14BuwFY8Mwkf7cFhF1uuxx", // 25. 0xabb11898
	"sp6JS7f14BuwFY8MwmCK2un99wb4e", // 26. 0xabb11898
	"sp6JS7f14BuwFY8MwmETztNHYu2Bx", // 27. 0xabb11898
	"sp6JS7f14BuwFY8MwmJws9UwRASfR", // 28. 0xabb11898
	"sp6JS7f14BuwFY8MwoH5PQkGK8tEb", // 29. 0xabb11898
	"sp6JS7f14BuwFY8MwoVXtP2yCzjJV", // 30. 0xabb11898
	"sp6JS7f14BuwFY8MwobxRXA9vsTeX", // 31. 0xabb11898
	"sp6JS7f14BuwFY8Mwos3pc5Gb3ihU", // 32. 0xabb11898
}

// Seeds for fixNFTokenDirV1 test - 17 in low group
var seedsSeventeenLo = []string{
	"sp6JS7f14BuwFY8Mw5EYu5z86hKDL", //  0. 0x399187e9
	"sp6JS7f14BuwFY8Mw5PUAMwc5ygd7", //  1. 0x399187e9
	"sp6JS7f14BuwFY8Mw5R3xUBcLSeTs", //  2. 0x399187e9
	"sp6JS7f14BuwFY8Mw5W6oS5sdC3oF", //  3. 0x399187e9
	"sp6JS7f14BuwFY8Mw5pYc3D9iuLcw", //  4. 0x399187e9
	"sp6JS7f14BuwFY8Mw5pfGVnhcdp3b", //  5. 0x399187e9
	"sp6JS7f14BuwFY8Mw6jS6RdEqXqrN", //  6. 0x399187e9
	"sp6JS7f14BuwFY8Mw6krt6AKbvRXW", //  7. 0x399187e9
	"sp6JS7f14BuwFY8Mw6mnVBQq7cAN2", //  8. 0x399187e9
	"sp6JS7f14BuwFY8Mw8ECJxPjmkufQ", //  9. 0x399187e9
	"sp6JS7f14BuwFY8Mw8asgzcceGWYm", // 10. 0x399187e9
	"sp6JS7f14BuwFY8MwF6J3FXnPCgL8", // 11. 0x399187e9
	"sp6JS7f14BuwFY8MwFEud2w5czv5q", // 12. 0x399187e9
	"sp6JS7f14BuwFY8MwFNxKVqJnx8P5", // 13. 0x399187e9
	"sp6JS7f14BuwFY8MwFnTCXg3eRidL", // 14. 0x399187e9
	"sp6JS7f14BuwFY8Mwj47hv1vrDge6", // 15. 0x399187e9
	"sp6JS7f14BuwFY8Mwj6TYekeeyukh", // 16. 0x399187e9
	"sp6JS7f14BuwFY8MwjYa5yLkgCLuT", // 17. 0xabb11898
	"sp6JS7f14BuwFY8MwjenxuJ3TH2Bc", // 18. 0xabb11898
	"sp6JS7f14BuwFY8MwjriN7Ui11NzB", // 19. 0xabb11898
	"sp6JS7f14BuwFY8Mwk3AuoJNSEo34", // 20. 0xabb11898
	"sp6JS7f14BuwFY8MwkT36hnRv8hTo", // 21. 0xabb11898
	"sp6JS7f14BuwFY8MwkTQixEXfi1Cr", // 22. 0xabb11898
	"sp6JS7f14BuwFY8MwkYJaZM1yTJBF", // 23. 0xabb11898
	"sp6JS7f14BuwFY8Mwkc4k1uo85qp2", // 24. 0xabb11898
	"sp6JS7f14BuwFY8Mwkf7cFhF1uuxx", // 25. 0xabb11898
	"sp6JS7f14BuwFY8MwmCK2un99wb4e", // 26. 0xabb11898
	"sp6JS7f14BuwFY8MwmETztNHYu2Bx", // 27. 0xabb11898
	"sp6JS7f14BuwFY8MwmJws9UwRASfR", // 28. 0xabb11898
	"sp6JS7f14BuwFY8MwoH5PQkGK8tEb", // 29. 0xabb11898
	"sp6JS7f14BuwFY8MwoVXtP2yCzjJV", // 30. 0xabb11898
	"sp6JS7f14BuwFY8MwobxRXA9vsTeX", // 31. 0xabb11898
	"sp6JS7f14BuwFY8Mwos3pc5Gb3ihU", // 32. 0xabb11898
}

// ===========================================================================
// testConsecutiveNFTs
// Reference: rippled NFTokenDir_test.cpp testConsecutiveNFTs
//
// Tests that it's possible to store many consecutive NFTs by manipulating
// the taxon so zero is always stored internally.
// ===========================================================================
func TestConsecutiveNFTs(t *testing.T) {
	env := jtx.NewTestEnv(t)

	issuer := jtx.NewAccount("issuer")
	buyer := jtx.NewAccount("buyer")

	// Reserve = 200 + ownerCount*50 XRP; buyer needs ~108 OwnerCount (100 offers + pages)
	env.FundAmount(issuer, 6000000000) // 6000 XRP
	env.FundAmount(buyer, 6000000000)  // 6000 XRP
	env.Close()

	const nftCount = 100

	nftIDs := make([]string, 0, nftCount)
	offerIndexes := make([]string, 0, nftCount)

	for i := 0; i < nftCount; i++ {
		// Tweak the taxon so zero is always stored internally:
		// taxon = cipheredTaxon(sequence, 0)
		// This produces tokens with sequential internal representations.
		tokenSeq := nft.GetNFTokenSeq(env, issuer)
		taxon := nftoken.CipheredTaxon(tokenSeq, 0)

		flags := nftoken.NFTokenFlagTransferable
		nftID := nft.GetNextNFTokenID(env, issuer, taxon, flags, 0)
		result := env.Submit(nft.NFTokenMint(issuer, taxon).Transferable().Build())
		if result.Success {
			nftIDs = append(nftIDs, nftID)
		}
		env.Close()
	}

	// Create a sell offer for each NFT
	for _, nftID := range nftIDs {
		offerIdx := nft.GetOfferIndex(env, issuer)
		result := env.Submit(nft.NFTokenCreateSellOffer(issuer, nftID, tx.NewXRPAmount(0)).
			Destination(buyer).Build())
		if result.Success {
			offerIndexes = append(offerIndexes, offerIdx)
		}
		env.Close()
	}

	// Buyer accepts all offers in reverse order
	for i := len(offerIndexes) - 1; i >= 0; i-- {
		result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offerIndexes[i]).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	// Verify all NFTs are findable by creating sell offers for them
	for _, nftID := range nftIDs {
		result := env.Submit(nft.NFTokenCreateSellOffer(buyer, nftID, tx.NewXRPAmount(100_000_000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
}

// exerciseLopsided is a helper for the lopsided split tests.
// Creates accounts from seeds, has each mint 1 NFT and offer to buyer.
// Buyer accepts all 33 offers - the 33rd may overflow.
func exerciseLopsided(t *testing.T, seeds []string) {
	env := jtx.NewTestEnv(t)

	buyer := jtx.NewAccount("buyer")
	// Reserve = 200 + ownerCount*50 XRP; buyer needs ~65 OwnerCount
	env.FundAmount(buyer, 4000000000) // 4000 XRP
	env.Close()

	// Create accounts from seeds and fund them all in the same ledger
	// (important: if fixNFTokenRemint is on, different ledgers = different sequences)
	accounts := make([]*jtx.Account, 0, len(seeds))
	for i, seed := range seeds {
		account := jtx.NewAccountFromSeed("acct"+string(rune('A'+i%26)), seed)
		accounts = append(accounts, account)
		env.Fund(account)
	}
	env.Close()

	// All accounts mint one NFT and offer it to buyer
	nftIDs := make([]string, 0, len(accounts))
	offers := make([]string, 0, len(accounts))

	for _, account := range accounts {
		flags := nftoken.NFTokenFlagTransferable
		nftID := nft.GetNextNFTokenID(env, account, 0, flags, 0)
		env.Submit(nft.NFTokenMint(account, 0).Transferable().Build())
		env.Close()

		offerIdx := nft.GetOfferIndex(env, account)
		env.Submit(nft.NFTokenCreateSellOffer(account, nftID, tx.NewXRPAmount(0)).
			Destination(buyer).Build())
		env.Close()

		nftIDs = append(nftIDs, nftID)
		offers = append(offers, offerIdx)
	}

	// Buyer accepts all offers
	for _, offer := range offers {
		result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offer).Build())
		// Last one may fail with tecNO_SUITABLE_NFTOKEN_PAGE - that's expected
		_ = result
		env.Close()
	}

	// Verify all accepted NFTs are findable
	for _, nftID := range nftIDs[:len(nftIDs)-1] {
		result := env.Submit(nft.NFTokenCreateSellOffer(buyer, nftID, tx.NewXRPAmount(100_000_000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
}

// ===========================================================================
// testLopsidedSplits
// Reference: rippled NFTokenDir_test.cpp testLopsidedSplits
//
// Tests that all NFT IDs with the same low 96 bits stay on the same page.
// ===========================================================================
func TestLopsidedSplits(t *testing.T) {
	t.Run("SplitAndAddToHi", func(t *testing.T) {
		exerciseLopsided(t, seedsSplitAndAddToHi)
	})

	t.Run("SplitAndAddToLo", func(t *testing.T) {
		exerciseLopsided(t, seedsSplitAndAddToLo)
	})
}

// ===========================================================================
// testFixNFTokenDirV1
// Reference: rippled NFTokenDir_test.cpp testFixNFTokenDirV1
//
// Exercises a fix for an off-by-one in NFTokenPage index creation.
// Without fixNFTokenDirV1: the 33rd NFT acceptance fails with
// tecNO_SUITABLE_NFTOKEN_PAGE. With fix: it succeeds.
// ===========================================================================
func TestFixNFTokenDirV1(t *testing.T) {
	exerciseFixDir := func(t *testing.T, seeds []string) {
		env := jtx.NewTestEnv(t)

		buyer := jtx.NewAccount("buyer")
		// Reserve = 200 + ownerCount*50 XRP; buyer needs ~69 OwnerCount
		env.FundAmount(buyer, 4000000000) // 4000 XRP
		env.Close()

		// Create accounts from seeds
		accounts := make([]*jtx.Account, 0, len(seeds))
		for i, seed := range seeds {
			account := jtx.NewAccountFromSeed("acct"+string(rune('A'+i%26)), seed)
			accounts = append(accounts, account)
			env.Fund(account)
		}
		env.Close()

		// All accounts create one NFT and offer to buyer
		nftIDs := make([]string, 0, len(accounts))
		offers := make([]string, 0, len(accounts))

		for _, account := range accounts {
			flags := nftoken.NFTokenFlagTransferable
			nftID := nft.GetNextNFTokenID(env, account, 0, flags, 0)
			env.Submit(nft.NFTokenMint(account, 0).Transferable().Build())
			env.Close()

			offerIdx := nft.GetOfferIndex(env, account)
			env.Submit(nft.NFTokenCreateSellOffer(account, nftID, tx.NewXRPAmount(0)).
				Destination(buyer).Build())
			env.Close()

			nftIDs = append(nftIDs, nftID)
			offers = append(offers, offerIdx)
		}

		// Buyer accepts all but the last offer
		for i := 0; i < len(offers)-1; i++ {
			result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offers[i]).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		// The last offer causes the page to split.
		// With fixNFTokenDirV1: tesSUCCESS
		// Without: tecNO_SUITABLE_NFTOKEN_PAGE
		result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offers[len(offers)-1]).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Verify all NFTs findable
		for _, nftID := range nftIDs {
			result := env.Submit(nft.NFTokenCreateSellOffer(buyer, nftID, tx.NewXRPAmount(100_000_000)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		// With fixNFTokenDirV1, buyer can also mint new NFTs
		result = env.Submit(nft.NFTokenMint(buyer, 0).Transferable().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	t.Run("SeventeenHi", func(t *testing.T) {
		exerciseFixDir(t, seedsSeventeenHi)
	})

	t.Run("SeventeenLo", func(t *testing.T) {
		exerciseFixDir(t, seedsSeventeenLo)
	})
}

// ===========================================================================
// testTooManyEquivalent
// Reference: rippled NFTokenDir_test.cpp testTooManyEquivalent
//
// Tests when 33 NFTs with identical sort characteristics are owned by
// the same account. Only 32 fit per page; the 33rd overflows.
// ===========================================================================
func TestTooManyEquivalent(t *testing.T) {
	env := jtx.NewTestEnv(t)

	buyer := jtx.NewAccount("buyer")
	// Reserve = 200 + ownerCount*50 XRP; buyer needs ~66 OwnerCount
	env.FundAmount(buyer, 4000000000) // 4000 XRP
	env.Close()

	// Create 33 accounts with identical low 32-bits
	accounts := make([]*jtx.Account, 0, len(seedsLow32_9a8ebed3))
	for i, seed := range seedsLow32_9a8ebed3 {
		account := jtx.NewAccountFromSeed("acct"+string(rune('A'+i%26)), seed)
		accounts = append(accounts, account)
		env.Fund(account)
	}
	env.Close()

	// All accounts create one NFT and offer to buyer
	nftIDs := make([]string, 0, len(accounts))
	offers := make([]string, 0, len(accounts))

	for _, account := range accounts {
		flags := nftoken.NFTokenFlagTransferable
		nftID := nft.GetNextNFTokenID(env, account, 0, flags, 0)
		env.Submit(nft.NFTokenMint(account, 0).Transferable().Build())
		env.Close()

		offerIdx := nft.GetOfferIndex(env, account)
		env.Submit(nft.NFTokenCreateSellOffer(account, nftID, tx.NewXRPAmount(0)).
			Destination(buyer).Build())
		env.Close()

		nftIDs = append(nftIDs, nftID)
		offers = append(offers, offerIdx)
	}

	// Remove the last NFT/offer - this will overflow
	overflowOffer := offers[len(offers)-1]
	acceptableNFTIDs := nftIDs[:len(nftIDs)-1]
	acceptableOffers := offers[:len(offers)-1]

	// Buyer accepts all but the overflow offer
	for _, offer := range acceptableOffers {
		result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offer).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}

	// Accepting the overflow offer should fail: page is full (32 tokens, all same low-96 bits)
	result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, overflowOffer).Build())
	jtx.RequireTxFail(t, result, "tecNO_SUITABLE_NFTOKEN_PAGE")

	// Verify all 32 NFTs are findable
	for _, nftID := range acceptableNFTIDs {
		result := env.Submit(nft.NFTokenCreateSellOffer(buyer, nftID, tx.NewXRPAmount(100_000_000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
}

// ===========================================================================
// testConsecutivePacking
// Reference: rippled NFTokenDir_test.cpp testConsecutivePacking
//
// Worst case scenario: 33 accounts with identical low-32 bits mint 7
// consecutive NFTs each. A single account buys all 7x32 of the 33 NFTs.
// Requires fixNFTokenDirV1.
// ===========================================================================
func TestConsecutivePacking(t *testing.T) {
	env := jtx.NewTestEnv(t)

	buyer := jtx.NewAccount("buyer")
	// Reserve = 200 + ownerCount*50 XRP; buyer needs ~459 OwnerCount
	env.FundAmount(buyer, 25000000000) // 25000 XRP
	env.Close()

	// Create 33 accounts with identical low 32-bits
	accounts := make([]*jtx.Account, 0, len(seedsLow32_115d0525))
	for i, seed := range seedsLow32_115d0525 {
		account := jtx.NewAccountFromSeed("acct"+string(rune('A'+i%26)), seed)
		accounts = append(accounts, account)
		env.Fund(account)
	}
	env.Close()

	// Each account creates 7 consecutive NFTs
	const nftsPerAccount = 7
	nftIDsByPage := make([][]string, nftsPerAccount)
	offersByPage := make([][]string, nftsPerAccount)

	for i := 0; i < nftsPerAccount; i++ {
		nftIDsByPage[i] = make([]string, 0, len(accounts))
		offersByPage[i] = make([]string, 0, len(accounts))

		for _, account := range accounts {
			// Tweak taxon so zero is always stored: cipheredTaxon(seq, 0)
			tokenSeq := nft.GetNFTokenSeq(env, account)
			taxon := nftoken.CipheredTaxon(tokenSeq, 0)

			flags := nftoken.NFTokenFlagTransferable
			nftID := nft.GetNextNFTokenID(env, account, taxon, flags, 0)
			env.Submit(nft.NFTokenMint(account, taxon).Transferable().Build())
			env.Close()

			offerIdx := nft.GetOfferIndex(env, account)
			env.Submit(nft.NFTokenCreateSellOffer(account, nftID, tx.NewXRPAmount(0)).
				Destination(buyer).Build())

			nftIDsByPage[i] = append(nftIDsByPage[i], nftID)
			offersByPage[i] = append(offersByPage[i], offerIdx)
		}
		env.Close()
	}

	// Remove one NFT/offer from each page - these would cause overflow
	overflowOffers := make([]string, nftsPerAccount)
	for i := 0; i < nftsPerAccount; i++ {
		lastIdx := len(nftIDsByPage[i]) - 1
		overflowOffers[i] = offersByPage[i][lastIdx]
		nftIDsByPage[i] = nftIDsByPage[i][:lastIdx]
		offersByPage[i] = offersByPage[i][:lastIdx]
	}

	// Buyer accepts all non-overflow offers
	// Fill center and outsides first to exercise different boundary cases
	for _, pageIdx := range []int{3, 6, 0, 1, 2, 5, 4} {
		for _, offer := range offersByPage[pageIdx] {
			result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offer).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}
	}

	// Accepting overflow offers should fail
	for _, offer := range overflowOffers {
		result := env.Submit(nft.NFTokenAcceptSellOffer(buyer, offer).Build())
		jtx.RequireTxFail(t, result, "tecNO_SUITABLE_NFTOKEN_PAGE")
		env.Close()
	}

	// Verify all expected NFTs are findable
	allNFTIDs := make([]string, 0)
	for _, page := range nftIDsByPage {
		allNFTIDs = append(allNFTIDs, page...)
	}
	sort.Strings(allNFTIDs)

	for _, nftID := range allNFTIDs {
		result := env.Submit(nft.NFTokenCreateSellOffer(buyer, nftID, tx.NewXRPAmount(100_000_000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	}
}
