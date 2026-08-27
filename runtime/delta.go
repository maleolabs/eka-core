package runtime

// DeltaCache implements client ETag delta cache by objectHash + instanceVersion
// (sto:budget-tooloutput-cache-real). objectHash immutable forever, byLine@instanceVersion via Timeline head check, If-None-Match → notModified or delta.

type DeltaCache struct {
	ByHash map[string][]byte
	ByLine map[string]int
}

func (c *DeltaCache) IsNotModified(canonicalForm, objectHash string, headVersion int) bool {
	if _, ok := c.ByHash[objectHash]; ok {
		return true
	}
	if v, ok := c.ByLine[canonicalForm]; ok && v == headVersion {
		return true
	}
	return false
}
