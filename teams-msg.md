## Env ELO Impact — Low-Tail Distribution Analysis

**7,310 environments** analyzed. The data is heavily right-skewed (skewness = 8.68) — a large number of environments have trivially few operations and inflate query length without contributing meaningful signal.

### Cut Point Options (removing low-operation environments)

| Keep environments with | Rows removed | Rows kept | Ops retained |
|---|---|---|---|
| **> 5 ops** | 2,087 (28.5%) | 5,223 (71.5%) | 97.8% |
| **> 10 ops** | 3,272 (44.7%) | 4,038 (55.3%) | 94.4% |
| **> 25 ops** | 4,887 (66.8%) | 2,423 (33.2%) | 84.6% |
| **> 50 ops** | 5,951 (81.4%) | 1,359 (18.6%) | 70.7% |

### Recommendation: cut at ≤ 10 ops

- Drops **44.7% of rows** but only loses **5.6% of total operations**
- Those 3,272 environments average ~5 ops each — basically inactive
- Retains **94.4%** of all operation volume in **55%** of the rows
- Best balance of query length reduction vs. signal preservation
