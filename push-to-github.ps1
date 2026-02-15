# Push Hintro to GitHub
# Run this AFTER creating the repo at https://github.com/new

Write-Host "Pushing to https://github.com/shivamshaw23/Hintro ..." -ForegroundColor Cyan
git push -u origin main

if ($LASTEXITCODE -eq 0) {
    Write-Host "`nSuccess! Your code is at https://github.com/shivamshaw23/Hintro" -ForegroundColor Green
} else {
    Write-Host "`nIf you haven't created the repo yet:" -ForegroundColor Yellow
    Write-Host "1. Open https://github.com/new?name=Hintro" -ForegroundColor White
    Write-Host "2. Click 'Create repository'" -ForegroundColor White
    Write-Host "3. Run this script again" -ForegroundColor White
}
