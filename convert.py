import pandas as pd

df = pd.read_csv(r"C:\Users\jokellih\dev\jacques\acis_data.csv")

df["operation_count"] = df["n1"] + df["n2"] + df["n3"]
df = df[["tenant_id", "env_id", "operation_count"]]

df.to_csv(r"C:\Users\jokellih\dev\jacques\env_elo_impact.csv", index=False)
print(f"Wrote {len(df)} rows to env_elo_impact.csv")
